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

// callerContextKey is the context key under which the authenticated caller is stored
type callerContextKey struct{}

// caller is the authenticated identity behind a request. globalAdmin is true for
// the config's admin token, which acts as a super-admin without a user record.
type caller struct {
	user        *store.User
	globalAdmin bool
	appScope    string // Non-empty: an app-scoped token, limited to this one app
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
	http.SetCookie(w, s.cookie(stateCookieName, state, int(oauthTimeout.Seconds()*40)))
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
	stateCookie, err := r.Cookie(stateCookieName)
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
	http.SetCookie(w, s.cookie(sessionCookieName, value, int(sessionTTL.Seconds())))
	http.SetCookie(w, s.cookie(stateCookieName, "", -1))
	http.Redirect(w, r, "/", http.StatusFound)
}

// handleLogout clears the session cookie
func (s *Server) handleLogout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, s.cookie(sessionCookieName, "", -1))
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
	cookie, err := r.Cookie(sessionCookieName)
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
	return &caller{user: u}, nil
}

// authenticated wraps a handler with authentication only (no status check), for
// endpoints pending users must reach, e.g. GET /v1/account
func (s *Server) authenticated(next func(http.ResponseWriter, *http.Request, *caller)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := s.authenticate(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, errors.New("please sign in"))
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), callerContextKey{}, c)), c)
	}
}

// requireActive wraps a handler so only approved accounts may proceed
func (s *Server) requireActive(next func(http.ResponseWriter, *http.Request, *caller)) http.HandlerFunc {
	return s.authenticated(func(w http.ResponseWriter, r *http.Request, c *caller) {
		if !c.isActive() {
			writeError(w, http.StatusForbidden, user.ErrNotActive)
			return
		}
		// An app-scoped token exists to be pasted into someone's agent, so it
		// may only reach the agent API for its own app -- never the account or
		// admin surface
		if c.appScope != "" && !strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusForbidden, errors.New("this token is limited to the app "+c.appScope+" and its /api/ endpoints"))
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

// cookie builds a cookie with the security attributes we always want
func (s *Server) cookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.config.TLS != "off",
		SameSite: http.SameSiteLaxMode,
	}
}
