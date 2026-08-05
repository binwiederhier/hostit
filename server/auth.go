package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"heckel.io/hostit/config"
	"heckel.io/hostit/store"
	"heckel.io/hostit/user"
)

const (
	googleAuthURL     = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL    = "https://oauth2.googleapis.com/token"
	googleUserInfoURL = "https://www.googleapis.com/oauth2/v3/userinfo"
	googleScopes      = "openid email profile"
	// oauthTimeout bounds the token exchange and userinfo calls
	oauthTimeout = 15 * time.Second
)

// caller is the authenticated identity behind a request. globalAdmin is true for
// the config's admin token, which acts as a super-admin without a user record.
type caller struct {
	user        *store.User
	globalAdmin bool
	appScope    string // Non-empty: an app-scoped token, limited to this one app
	viaCookie   bool   // Authenticated by session cookie, so a browser may have sent it unasked
}

// isAdmin reports whether the caller may manage other users and global settings
func (c *caller) isAdmin() bool {
	return c.globalAdmin || (c.user != nil && c.user.Role == store.RoleAdmin)
}

// isActive reports whether the caller may use the platform (approved account).
// An app token for an ownerless app has no user record behind it, but is still
// a legitimate caller for its own app.
func (c *caller) isActive() bool {
	if c.globalAdmin || (c.user != nil && c.user.Status == store.StatusActive) {
		return true
	}
	return c.user == nil && c.appScope != ""
}

// userID returns the caller's user ID, empty for the global admin token
func (c *caller) userID() string {
	if c.user == nil {
		return ""
	}
	return c.user.ID
}

// googleIdentity is the subset of Google's userinfo response we need
type googleIdentity struct {
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
}

// handleGoogleLogin redirects to Google's consent screen with a state cookie
func (s *Server) handleGoogleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.config.WebEnabled() {
		writeError(w, http.StatusNotImplemented, errors.New("web login is not configured on this server"))
		return
	}
	state := randomToken()
	http.SetCookie(w, s.cookie(s.cookieName(stateCookieName), state, int(oauthTimeout.Seconds()*40)))
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
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleLogout clears the session cookie
func (s *Server) handleLogout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, s.cookie(s.cookieName(sessionCookieName), "", -1))
	writeJSON(w, http.StatusOK, &apiMessageResponse{Message: "logged out"})
}

// exchangeGoogleCodeLive trades an authorization code for the user's identity;
// tests replace this via the Server.exchangeGoogleCode field
func (s *Server) exchangeGoogleCodeLive(code, host string) (*googleIdentity, error) {
	ctx, cancel := context.WithTimeout(context.Background(), oauthTimeout)
	defer cancel()
	form := url.Values{
		"code":          {code},
		"client_id":     {s.config.GoogleClientID},
		"client_secret": {s.config.GoogleClientSecret},
		"redirect_uri":  {s.config.RedirectURL(host)},
		"grant_type":    {"authorization_code"},
	}
	req, err := http.NewRequestWithContext(ctx, "POST", googleTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange failed with HTTP %d", resp.StatusCode)
	}
	var tokenResp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, err
	}

	// Fetch the verified identity with the access token
	infoReq, err := http.NewRequestWithContext(ctx, "GET", googleUserInfoURL, nil)
	if err != nil {
		return nil, err
	}
	infoReq.Header.Set("Authorization", "Bearer "+tokenResp.AccessToken)
	infoResp, err := http.DefaultClient.Do(infoReq)
	if err != nil {
		return nil, err
	}
	defer infoResp.Body.Close()
	if infoResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo request failed with HTTP %d", infoResp.StatusCode)
	}
	var identity googleIdentity
	if err := json.NewDecoder(infoResp.Body).Decode(&identity); err != nil {
		return nil, err
	}
	return &identity, nil
}

// authenticate resolves the caller from a session cookie or bearer token; it
// does NOT check status or role (see requireActive / requireAdmin)
func (s *Server) authenticate(r *http.Request) (*caller, error) {
	if token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "); token != "" && token != r.Header.Get("Authorization") {
		if subtle.ConstantTimeCompare([]byte(token), []byte(s.config.AdminToken)) == 1 {
			return &caller{globalAdmin: true}, nil
		}
		u, scope, err := s.users.UserAndScopeByToken(token)
		if err != nil {
			return nil, err
		}
		return &caller{user: u, appScope: scope}, nil
	}
	cookie, err := r.Cookie(s.cookieName(sessionCookieName))
	if err != nil || cookie.Value == "" {
		return nil, errInvalidSession
	}
	userID, err := s.sessions.decode(cookie.Value)
	if err != nil {
		return nil, err
	}
	u, err := s.users.User(userID)
	if err != nil {
		return nil, err
	}
	return &caller{user: u, viaCookie: true}, nil
}

// authenticated wraps a handler with authentication only (no status check), for
// endpoints pending users must reach, e.g. GET /api/account
func (s *Server) authenticated(next func(http.ResponseWriter, *http.Request, *caller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := s.authenticate(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, errors.New("please sign in"))
			return
		}
		if err := s.checkSameOrigin(r, c); err != nil {
			writeError(w, http.StatusForbidden, err)
			return
		}
		// An app-scoped token exists to be pasted into someone's agent, so it may
		// only reach its own app's endpoints. This belongs here rather than in
		// requireActive: /api/account is authenticated-only (pending users must
		// see why they are waiting), and would otherwise skip the check entirely.
		if c.appScope != "" && !withinAppScope(r.URL.Path, c.appScope) {
			writeError(w, http.StatusForbidden, errors.New("this token is limited to the app "+c.appScope+", under "+apiPrefix+"/apps/"+c.appScope+"/"))
			return
		}
		next(w, r, c)
	}
}

// requireActive wraps a handler so only approved accounts may proceed
func (s *Server) requireActive(next func(http.ResponseWriter, *http.Request, *caller)) http.HandlerFunc {
	return s.authenticated(func(w http.ResponseWriter, r *http.Request, c *caller) {
		if !c.isActive() {
			writeError(w, http.StatusForbidden, user.ErrNotActive)
			return
		}
		next(w, r, c)
	})
}

// requireAdmin wraps a handler so only admins may proceed
func (s *Server) requireAdmin(next func(http.ResponseWriter, *http.Request, *caller)) http.HandlerFunc {
	return s.requireActive(func(w http.ResponseWriter, r *http.Request, c *caller) {
		if !c.isAdmin() {
			writeError(w, http.StatusForbidden, errors.New("administrator access required"))
			return
		}
		next(w, r, c)
	})
}

// withinAppScope reports whether a path belongs to one app's own endpoints. The
// prefix is the permission: an app token that could reach /api/account would
// tell whoever it was pasted to who its owner is.
func withinAppScope(path, app string) bool {
	// The platform guide explains the API to whoever holds the token and says
	// nothing about any particular app, so an agent can always read it
	if path == apiPrefix+"/info" {
		return true
	}
	prefix := apiPrefix + "/apps/" + app
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// checkSameOrigin refuses a state-changing request that a browser sent on its
// own initiative. Apps are subdomains of the web app, so SameSite=Lax does not
// separate them: without this, a page served by one tenant could POST as
// whoever is signed in -- including an admin creating an account for them.
// Token-authenticated callers are unaffected; nobody rides along on those.
func (s *Server) checkSameOrigin(r *http.Request, c *caller) error {
	if !c.viaCookie || r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return nil
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return nil
	case "": // Old browser or a non-browser client: fall back to Origin
		if origin := r.Header.Get("Origin"); origin == "" || s.isWebOrigin(origin) {
			return nil
		}
	}
	return errors.New("cross-site request refused; use an API token instead of a session")
}

// isWebOrigin reports whether an Origin header names one of our own hostnames
func (s *Server) isWebOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return s.config.IsWebHostname(hostOnly(u.Host))
}

// cookieName prefixes a cookie with __Host- where the browser will accept it.
// The prefix requires Secure, so a plain-HTTP deployment (local development)
// keeps the bare name rather than setting a cookie no browser will store.
func (s *Server) cookieName(name string) string {
	if s.config.TLS == config.TLSOff {
		return name
	}
	return hostCookiePrefix + name
}

// cookie builds a cookie with the security attributes we always want
func (s *Server) cookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.config.TLS != config.TLSOff,
		SameSite: http.SameSiteLaxMode,
	}
}
