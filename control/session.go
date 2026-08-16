package control

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// sessionCookieName holds the signed session; stateCookieName the OAuth state.
	// The __Host- prefix is what keeps an app subdomain from planting a session
	// on the web app: browsers refuse such a cookie if it carries a Domain
	// attribute. It also requires Secure, so plain-HTTP setups drop the prefix.
	sessionCookieName = "hostit_session"
	stateCookieName   = "hostit_state"
	hostCookiePrefix  = "__Host-"
	// sessionTTL is how long a web login lasts
	sessionTTL = 30 * 24 * time.Hour
)

var (
	errInvalidSession = errors.New("invalid or expired session")
)

// sessionManager signs and verifies session cookies. Sessions are stateless:
// the cookie carries "<userID>|<expiry>|<hmac>", so no server-side storage and
// no cleanup, at the cost of not being individually revocable before expiry.
type sessionManager struct {
	key []byte
	ttl time.Duration
}

func newSessionManager(key string) *sessionManager {
	return &sessionManager{
		key: []byte(key),
		ttl: sessionTTL,
	}
}

// encode returns a signed cookie value for the given user
func (s *sessionManager) encode(userID string) (string, error) {
	if strings.Contains(userID, "|") {
		return "", fmt.Errorf("invalid user id %q", userID)
	}
	payload := fmt.Sprintf("%s|%d", userID, time.Now().Add(s.ttl).Unix())
	return payload + "|" + s.sign(payload), nil
}

// decode verifies a cookie value and returns the user ID it carries
func (s *sessionManager) decode(value string) (string, error) {
	parts := strings.Split(value, "|")
	if len(parts) != 3 {
		return "", errInvalidSession
	}
	payload, signature := parts[0]+"|"+parts[1], parts[2]
	if !hmac.Equal([]byte(signature), []byte(s.sign(payload))) {
		return "", errInvalidSession
	}
	expiry, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", errInvalidSession
	}
	if time.Now().After(time.Unix(expiry, 0)) {
		return "", errInvalidSession
	}
	return parts[0], nil
}

func (s *sessionManager) sign(payload string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// randomToken returns a random URL-safe string, used for OAuth state values
func randomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err) // Only fails if the system entropy source is broken
	}
	return hex.EncodeToString(b)
}
