package control

import (
	"errors"
	"fmt"
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
	if !s.config.WebEnabled() {
		writeError(w, http.StatusNotImplemented, errors.New("web login is not configured on this server"))
		return
	}
	stateCookie, err := r.Cookie(s.cookieName(stateCookieName))
	if err != nil || stateCookie.Value == "" || stateCookie.Value != r.URL.Query().Get("state") {
		writeError(w, http.StatusBadRequest, errors.New("invalid login state, please try again"))
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
// "breakglass" config flag is set. It will only sign in an account that already
// exists; a configured admin email is additionally allowed to be created on the
// spot, exactly as a first Google login would.
func (s *Server) handleBreakglass(w http.ResponseWriter, r *http.Request) {
	if !s.config.Breakglass {
		writeError(w, http.StatusNotFound, errors.New("breakglass login is not enabled"))
		return
	}
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
