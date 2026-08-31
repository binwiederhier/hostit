package control

import (
	"errors"
	"fmt"
	"log/slog"

	"heckel.io/hostit/store"
	"net/http"
	"net/url"
)

// The web login handlers: Google OAuth, the admin-token breakglass login and
// logout. The caller type, middleware, session helpers and cookie plumbing
// they build on live in auth.go.

// handleGoogleLogin redirects to Google's consent screen with a state cookie
func (s *Server) handleGoogleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.config.WebEnabled() {
		writeError(w, http.StatusNotImplemented, errors.New("web login is not configured on this server"))
		return
	}
	state := randomToken()
	http.SetCookie(w, s.cookie(s.cookieName(stateCookieName), state, int(oauthTimeout.Seconds()*40)))
	// Where to land afterwards, when the visitor was on their way somewhere --
	// a private app sends them here to sign in first. localPath keeps it on this
	// host: /auth/google is a plain GET, so the target is attacker-supplied.
	if next := r.URL.Query().Get(nextParam); next != "" {
		http.SetCookie(w, s.cookie(s.cookieName(nextCookieName), localPath(next), int(oauthTimeout.Seconds()*40)))
	}
	params := url.Values{
		"client_id":     {s.config.GoogleClientID},
		"redirect_uri":  {s.config.RedirectURL(hostOnly(r.Host))},
		"response_type": {"code"},
		"scope":         {googleScopes},
		"state":         {state},
		"prompt":        {"select_account"},
	}
	http.Redirect(w, r, googleAuthURL+"?"+params.Encode(), http.StatusFound)
}

// handleGoogleCallback verifies the state, exchanges the code for the user's
// identity, and issues a session cookie
func (s *Server) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	// The state check comes first and applies to BOTH flows: it is the CSRF
	// defence, and skipping it for either would be the bug it exists to stop.
	stateCookie, err := r.Cookie(s.cookieName(stateCookieName))
	if err != nil || stateCookie.Value == "" || stateCookie.Value != r.URL.Query().Get("state") {
		writeError(w, http.StatusBadRequest, errors.New("invalid login state, please try again"))
		return
	}
	// A connection's consent comes back here too, so that connecting an account
	// needs no second redirect URI registered with the provider. The state says
	// which flow this is; anything else is an ordinary login.
	//
	// Checked BEFORE the login guard on purpose: an instance can offer Slack or
	// Jira connections without Google login configured at all, and refusing the
	// callback in that case would break a flow that has nothing to do with
	// Google.
	if s.connectionFromState(w, r, stateCookie.Value) {
		return
	}
	if !s.config.WebEnabled() {
		writeError(w, http.StatusNotImplemented, errors.New("web login is not configured on this server"))
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, errors.New("missing authorization code"))
		return
	}
	identity, err := s.exchangeGoogleCode(code, hostOnly(r.Host))
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Errorf("cannot verify Google login: %w", err))
		return
	}
	if !identity.EmailVerified {
		writeError(w, http.StatusForbidden, errors.New("your Google account has no verified email address"))
		return
	}
	u, err := s.users.Login(identity.Email, identity.Name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.resolvePendingViewers(u)
	value, err := s.sessions.encode(u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	http.SetCookie(w, s.cookie(s.cookieName(sessionCookieName), value, int(sessionTTL.Seconds())))
	http.SetCookie(w, s.cookie(s.cookieName(stateCookieName), "", -1))
	http.SetCookie(w, s.cookie(s.cookieName(nextCookieName), "", -1))
	http.Redirect(w, r, s.afterLogin(r), http.StatusFound)
}

// handleBreakglass mints a normal session cookie for a user, authorized by the
// admin token alone -- no Google round-trip. It exists for e2e testing (viewing
// the app as any real user) and recovery, and grants nothing new: the admin token
// already carries full admin rights over the REST API. It is off unless the
// It will only sign in an account that already
// exists; a configured admin email is additionally allowed to be created on the
// spot, exactly as a first Google login would.
func (s *Server) handleBreakglass(w http.ResponseWriter, r *http.Request) {
	// Always available; the admin token is the gate (see below). It only signs in
	// an account that already exists, so it cannot conjure access to a new email.
	c, err := s.authenticate(r)
	if err != nil || !c.globalAdmin {
		writeError(w, http.StatusForbidden, errors.New("breakglass login requires the admin token"))
		return
	}
	email := r.URL.Query().Get("email")
	u, err := s.users.UserByEmail(email)
	if err != nil {
		// No such account: create one only for a configured admin email, the same
		// way their first Google login would; refuse to conjure arbitrary users.
		if !s.config.IsAdminEmail(email) {
			writeError(w, http.StatusForbidden, errors.New("no account for that email"))
			return
		}
		if u, err = s.users.Login(email, email); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	s.resolvePendingViewers(u)
	value, err := s.sessions.encode(u.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	http.SetCookie(w, s.cookie(s.cookieName(sessionCookieName), value, int(sessionTTL.Seconds())))
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "signed in as " + email})
}

// handleLogout clears the session cookie
func (s *Server) handleLogout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, s.cookie(s.cookieName(sessionCookieName), "", -1))
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "logged out"})
}

// afterLogin is where a finished login lands: back where the visitor was
// headed if they were sent here from somewhere, otherwise the dashboard.
func (s *Server) afterLogin(r *http.Request) string {
	cookie, err := r.Cookie(s.cookieName(nextCookieName))
	if err != nil || cookie.Value == "" {
		return "/"
	}
	return localPath(cookie.Value)
}

// resolvePendingViewers turns any pending view invites for this person's email
// into real grants, now that they have an account. Best effort: a failure here
// must not block their sign-in.
func (s *Server) resolvePendingViewers(u *store.User) {
	if u == nil {
		return
	}
	n, err := s.apps.Store().ResolvePendingViewers(u.Email, u.ID)
	if err != nil {
		slog.Warn("Cannot resolve pending viewer invites", "email", u.Email, "error", err)
		return
	}
	if n == 0 {
		return
	}
	slog.Info("Resolved pending viewer invites on sign-in", "email", u.Email, "apps", n)
	// Someone invited only to view: their account exists purely for that grant,
	// so activate them as a viewer rather than leaving them pending for approval.
	// Only a fresh, unprivileged, still-pending account is converted -- an
	// allowed-domain user (already active) or an admin is left exactly as it is.
	if u.Status == store.StatusPending && u.Role == store.RoleUser {
		u.Role, u.Status = store.RoleViewer, store.StatusActive
		if err := s.users.Update(u); err != nil {
			slog.Warn("Cannot activate invited viewer", "email", u.Email, "error", err)
		}
	}
}
