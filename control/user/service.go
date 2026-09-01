// Package user manages people: Google-authenticated accounts, their approval
// status and role, per-user limits (with global defaults), API tokens and
// profile SSH keys.
package user

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/crypto/ssh"

	"heckel.io/hostit/control/config"
	"heckel.io/hostit/store"
)

const (
	// tokenPrefix marks hostit API tokens; the visible prefix stored alongside
	// the hash is tokenPrefix plus tokenPrefixChars characters
	tokenPrefix      = "hostit_"
	tokenBytes       = 24
	tokenPrefixChars = 6

	// Built-in limits, used when no global setting and no per-user override exists
	// Small on purpose: a fresh app is usually a static site or a tiny
	// service, and owners can raise limits within their pool. Existing apps
	// were pinned at their old effective limits when these shrank (2026-08-21
	// migration), so a default change never resizes something already running.
	defaultAppLimit = 3
	defaultMemoryMB = 128
	defaultDiskMB   = 256
	// The pools are their OWN defaults, deliberately not app_limit x the
	// per-app size: an admin states "every user gets a gigabyte", and raising
	// the per-app default must not silently raise everyone's ceiling.
	defaultMemoryPoolMB = 1024
	defaultDiskPoolMB   = 4096

	settingAppLimit     = "default_app_limit"
	settingMemoryMB     = "default_memory_mb"
	settingDiskMB       = "default_disk_mb"
	settingMemoryPoolMB = "default_memory_pool_mb"
	settingDiskPoolMB   = "default_disk_pool_mb"
)

var (
	// ErrNotActive is returned when a pending or denied user tries to do anything
	ErrNotActive = errors.New("account is not active: an administrator must approve it")
	// ErrInvalid marks validation errors (bad SSH keys, bad limit values)
	ErrInvalid = errors.New("invalid request")

	// domainRe and emailRe are deliberately loose: Google has already verified
	// the address, so these only need to catch typos in the admin UI
	domainRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
	emailRe  = regexp.MustCompile(`^[^@\s]+@[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)
)

// Limits are the effective per-user resource limits
type Limits struct {
	AppLimit int `json:"app_limit"`
	MemoryMB int `json:"memory_mb"`
	DiskMB   int `json:"disk_mb"`
	// The pools bound the SUM of the user's apps' effective limits. A user row
	// without an explicit pool gets the instance default; 0 means unbounded.
	MemoryPoolMB int `json:"memory_pool_mb"`
	DiskPoolMB   int `json:"disk_pool_mb"`
}

// EffectiveAppLimits resolves what one app is actually held to: its own
// admin- or owner-set overrides where present, else the resolved per-app
// defaults. CPU has no inheritable default, so the override is the whole
// story (0 = uncapped). Every consumer -- the API display, the desired state
// control asserts, startup priming and the pool math -- goes through here;
// a private copy of this logic is how a daemon restart once quietly
// un-applied an admin's cap.
func EffectiveAppLimits(l *Limits, a *store.App) (memoryMB, diskMB, cpuMilli int) {
	memoryMB, diskMB = l.MemoryMB, l.DiskMB
	if a.MemoryLimitMB > 0 {
		memoryMB = a.MemoryLimitMB
	}
	if a.DiskLimitMB > 0 {
		diskMB = a.DiskLimitMB
	}
	return memoryMB, diskMB, a.CPUMilli
}

// Manager owns users, tokens, profile keys and limit settings
type Manager struct {
	config *config.Config
	store  *store.Store
}

// NewManager creates a Manager
func NewManager(conf *config.Config, s *store.Store) *Manager {
	return &Manager{
		config: conf,
		store:  s,
	}
}

// Login finds or creates the user behind a verified Google identity. New users
// start pending; emails listed in the config's admin-emails are auto-approved
// as admins (and existing accounts are promoted), and addresses in an allowed
// domain are approved as ordinary users.
func (m *Manager) Login(email, name string) (*store.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return nil, fmt.Errorf("%w: empty email", ErrInvalid)
	}
	isAdminEmail := slices.Contains(m.adminEmails(), email)
	allowed, err := m.emailDomainAllowed(email)
	if err != nil {
		return nil, err
	}
	u, err := m.store.UserByEmail(email)
	if errors.Is(err, store.ErrUserNotFound) {
		u = &store.User{Email: email, Name: name, Role: store.RoleUser, Status: store.StatusPending}
		if allowed {
			u.Status = store.StatusActive
		}
		if isAdminEmail {
			u.Role, u.Status = store.RoleAdmin, store.StatusActive
		}
		if err := m.store.AddUser(u); err != nil {
			return nil, err
		}
		return u, nil
	} else if err != nil {
		return nil, err
	}
	if name != "" {
		u.Name = name
	}
	// Only pending accounts are swept up by a newly allowed domain: someone the
	// admin turned away stays denied until the admin says otherwise
	if allowed && u.Status == store.StatusPending {
		u.Status = store.StatusActive
	}
	if isAdminEmail {
		u.Role, u.Status = store.RoleAdmin, store.StatusActive
	}
	if err := m.store.UpdateUser(u); err != nil {
		return nil, err
	}
	return u, nil
}

// Invite creates an approved account before its owner has ever signed in, so an
// admin can hand out access directly. The name stays empty until the first
// Google sign-in fills it in.
func (m *Manager) Invite(email string, role store.Role) (*store.User, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if !emailRe.MatchString(email) {
		return nil, fmt.Errorf("%w: %q is not an email address", ErrInvalid, email)
	}
	if role != store.RoleAdmin && role != store.RoleUser {
		return nil, fmt.Errorf("%w: unknown role %q", ErrInvalid, role)
	}
	if _, err := m.store.UserByEmail(email); err == nil {
		return nil, fmt.Errorf("%w: %s already has an account", ErrInvalid, email)
	} else if !errors.Is(err, store.ErrUserNotFound) {
		return nil, err
	}
	u := &store.User{Email: email, Role: role, Status: store.StatusActive}
	if err := m.store.AddUser(u); err != nil {
		return nil, err
	}
	return u, nil
}

// AllowDomain approves everyone signing in with an address in this domain.
// Admins think in terms of "*@company.com", so accept that shape (and
// "@company.com") alongside the bare domain.
func (m *Manager) AllowDomain(domain string) (*store.AllowedDomain, error) {
	domain, err := normalizeDomain(domain)
	if err != nil {
		return nil, err
	}
	if publicMailProviders[domain] {
		return nil, fmt.Errorf("%w: %s is a public email provider -- allowing it would let anyone in, not just your organisation", ErrInvalid, domain)
	}
	d := &store.AllowedDomain{Domain: domain}
	if err := m.store.AddAllowedDomain(d); err != nil {
		return nil, err
	}
	return d, nil
}

// AllowedDomains lists the domains that skip the approval queue
func (m *Manager) AllowedDomains() ([]*store.AllowedDomain, error) {
	return m.store.AllowedDomains()
}

// DisallowDomain stops auto-approving a domain; accounts already approved under
// it keep working, since revoking access is a per-user decision
func (m *Manager) DisallowDomain(domain string) error {
	domain, err := normalizeDomain(domain)
	if err != nil {
		return err
	}
	return m.store.RemoveAllowedDomain(domain)
}

// emailDomainAllowed reports whether an address belongs to an allowed domain
func (m *Manager) emailDomainAllowed(email string) (bool, error) {
	at := strings.LastIndex(email, "@")
	if at < 0 {
		return false, nil
	}
	return m.store.DomainAllowed(email[at+1:])
}

// normalizeDomain turns what an admin types into a bare lowercase domain
func normalizeDomain(domain string) (string, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	domain = strings.TrimPrefix(domain, "*")
	domain = strings.TrimPrefix(domain, "@")
	if !domainRe.MatchString(domain) {
		return "", fmt.Errorf("%w: %q is not a domain (try company.com or *@company.com)", ErrInvalid, domain)
	}
	return domain, nil
}

// User returns a user by ID
func (m *Manager) User(id string) (*store.User, error) {
	return m.store.User(id)
}

// UserByEmail looks up a user by their email address
func (m *Manager) UserByEmail(email string) (*store.User, error) {
	return m.store.UserByEmail(email)
}

// Users returns all users
func (m *Manager) Users() ([]*store.User, error) {
	return m.store.Users()
}

// Update persists changes to a user (role, status, limit overrides)
func (m *Manager) Update(u *store.User) error {
	return m.store.UpdateUser(u)
}

// Delete removes a user; the caller must delete their apps first
func (m *Manager) Delete(id string) error {
	return m.store.RemoveUser(id)
}

// Limits returns the effective limits for a user: per-user override, else the
// global setting, else the built-in default
func (m *Manager) Limits(u *store.User) (*Limits, error) {
	defaults, err := m.Defaults()
	if err != nil {
		return nil, err
	}
	limits := *defaults
	if u.AppLimit != nil {
		limits.AppLimit = *u.AppLimit
	}
	if u.MemoryMB != nil {
		limits.MemoryMB = *u.MemoryMB
	}
	if u.DiskMB != nil {
		limits.DiskMB = *u.DiskMB
	}
	// Pools, most specific wins: the user's own, else the instance default.
	// No derivation from the per-app sizes -- they are independent knobs.
	if u.MemoryPoolMB != nil {
		limits.MemoryPoolMB = *u.MemoryPoolMB
	}
	if u.DiskPoolMB != nil {
		limits.DiskPoolMB = *u.DiskPoolMB
	}
	// A viewer never owns apps, whatever app_limit happens to be stored: force it
	// to zero so nothing downstream can be talked into letting one create.
	if u.Role == store.RoleViewer {
		limits.AppLimit = 0
	}
	return &limits, nil
}

// positiveOr keeps a configured value only when it is a real one.
func positiveOr(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

// Defaults returns the global default limits
func (m *Manager) Defaults() (*Limits, error) {
	settings, err := m.store.Settings()
	if err != nil {
		return nil, err
	}
	return &Limits{
		AppLimit: settingInt(settings, settingAppLimit, defaultAppLimit),
		MemoryMB: settingInt(settings, settingMemoryMB, defaultMemoryMB),
		DiskMB:   settingInt(settings, settingDiskMB, defaultDiskMB),
		// A stored 0 means "not configured" and falls back to the built-in: an
		// instance-wide 0 would switch pool enforcement off for everyone, which
		// is never what saving a form was meant to do. A single USER's pool may
		// still be 0 to mean unlimited -- that is a deliberate, per-person act.
		MemoryPoolMB: positiveOr(settingInt(settings, settingMemoryPoolMB, 0), defaultMemoryPoolMB),
		DiskPoolMB:   positiveOr(settingInt(settings, settingDiskPoolMB, 0), defaultDiskPoolMB),
	}, nil
}

// SetDefaults updates the global default limits
func (m *Manager) SetDefaults(limits *Limits) error {
	if limits.AppLimit < 0 || limits.MemoryMB < 0 || limits.DiskMB < 0 || limits.MemoryPoolMB < 0 || limits.DiskPoolMB < 0 {
		return fmt.Errorf("%w: limits must not be negative", ErrInvalid)
	}
	if err := m.store.SetSetting(settingAppLimit, strconv.Itoa(limits.AppLimit)); err != nil {
		return err
	}
	if err := m.store.SetSetting(settingMemoryMB, strconv.Itoa(limits.MemoryMB)); err != nil {
		return err
	}
	if err := m.store.SetSetting(settingDiskMB, strconv.Itoa(limits.DiskMB)); err != nil {
		return err
	}
	if err := m.store.SetSetting(settingMemoryPoolMB, strconv.Itoa(limits.MemoryPoolMB)); err != nil {
		return err
	}
	return m.store.SetSetting(settingDiskPoolMB, strconv.Itoa(limits.DiskPoolMB))
}

// CreateToken issues a new account-wide API token; the returned string is shown
// to the user exactly once, only its hash is stored
func (m *Manager) CreateToken(userID, label string) (string, *store.Token, error) {
	return m.CreateAppToken(userID, "", label)
}

// CreateAppToken issues a token limited to a single app. These are what the web
// app hands out for agents: the user pastes it into their own chat session, so
// it must not be able to touch their other apps.
func (m *Manager) CreateAppToken(userID, appName, label string) (string, *store.Token, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token := tokenPrefix + hex.EncodeToString(b)
	tk := &store.Token{
		UserID:  userID,
		Hash:    hashToken(token),
		Prefix:  token[:len(tokenPrefix)+tokenPrefixChars],
		Label:   label,
		AppName: appName,
	}
	if appName != "" {
		// An app token is meant to be pasted into the owner's agent, so its page
		// must be able to show it again; account-wide tokens stay hash-only
		tk.Secret = token
	}
	if err := m.store.AddToken(tk); err != nil {
		return "", nil, err
	}
	return token, tk, nil
}

// UserByToken resolves an API token to its (active) user and records usage
func (m *Manager) UserByToken(token string) (*store.User, error) {
	u, _, err := m.UserAndScopeByToken(token)
	return u, err
}

// UserAndScopeByToken resolves an API token to its (active) user and the app it
// is limited to; an empty scope means the token covers the whole account
func (m *Manager) UserAndScopeByToken(token string) (*store.User, string, error) {
	tk, err := m.store.TokenByHash(hashToken(token))
	if err != nil {
		return nil, "", err
	}
	// An app created with the global admin token has no owner, but its agent
	// token must still work: it is scoped to that app and nothing else
	if tk.UserID == "" {
		return nil, tk.AppName, nil
	}
	u, err := m.store.User(tk.UserID)
	if err != nil {
		return nil, "", err
	}
	if u.Status != store.StatusActive {
		return nil, "", ErrNotActive
	}
	if err := m.store.TouchToken(tk.ID); err != nil {
		return nil, "", err
	}
	return u, tk.AppName, nil
}

// AppToken returns the token scoped to an app, creating one if it has none.
// Apps get a token the moment they are created, so the owner never has to think
// about credentials before handing the app to an agent.
func (m *Manager) AppToken(userID, appName string) (string, error) {
	tokens, err := m.store.TokensByApp(appName)
	if err != nil {
		return "", err
	}
	for _, tk := range tokens {
		if tk.Secret != "" {
			return tk.Secret, nil
		}
	}
	token, _, err := m.CreateAppToken(userID, appName, "agent")
	return token, err
}

// RotateAppToken replaces an app's token, invalidating the previous one
func (m *Manager) RotateAppToken(userID, appName string) (string, error) {
	tokens, err := m.store.TokensByApp(appName)
	if err != nil {
		return "", err
	}
	for _, tk := range tokens {
		if err := m.store.RemoveToken(tk.UserID, tk.ID); err != nil {
			return "", err
		}
	}
	token, _, err := m.CreateAppToken(userID, appName, "agent")
	return token, err
}

// Tokens lists a user's tokens (hashes are never exposed)
func (m *Manager) Tokens(userID string) ([]*store.Token, error) {
	return m.store.TokensByUser(userID)
}

// DeleteToken revokes one of the user's own tokens
func (m *Manager) DeleteToken(userID, id string) error {
	return m.store.RemoveToken(userID, id)
}

// AddKey adds an SSH public key to the user's profile; it grants access to all
// apps that user owns
func (m *Manager) AddKey(userID, label, key string) (*store.UserKey, error) {
	key = strings.TrimSpace(key)
	// A single key only: reject an embedded newline, and reject anything the
	// parser leaves behind (a second key on another line). Either would smuggle
	// an extra authorized_keys entry that survives revocation of the first.
	if strings.ContainsAny(key, "\n\r") {
		return nil, fmt.Errorf("%w: an SSH key must be a single line", ErrInvalid)
	}
	if _, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(key)); err != nil || len(strings.TrimSpace(string(rest))) != 0 {
		return nil, fmt.Errorf("%w: not a valid SSH public key", ErrInvalid)
	}
	if label == "" {
		label = "key"
	}
	k := &store.UserKey{UserID: userID, Label: label, Key: key}
	if err := m.store.AddUserKey(k); err != nil {
		return nil, err
	}
	return k, nil
}

// Keys lists a user's profile SSH keys
func (m *Manager) Keys(userID string) ([]*store.UserKey, error) {
	return m.store.UserKeys(userID)
}

// KeyStrings returns just the key material of a user's profile keys
func (m *Manager) KeyStrings(userID string) ([]string, error) {
	keys, err := m.store.UserKeys(userID)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k.Key)
	}
	return out, nil
}

// DeleteKey removes one of the user's own profile keys
// RenameKey changes an SSH key's label, which is only ever how a person tells
// one from another -- the key itself is untouched.
func (m *Manager) RenameKey(userID, id, label string) error {
	label = strings.TrimSpace(label)
	if label == "" {
		return fmt.Errorf("%w: a label is required", ErrInvalid)
	}
	return m.store.RenameUserKey(userID, id, label)
}

func (m *Manager) DeleteKey(userID, id string) error {
	return m.store.RemoveUserKey(userID, id)
}

// adminEmails returns the configured admin emails, normalized
func (m *Manager) adminEmails() []string {
	emails := make([]string, 0, len(m.config.AdminEmails))
	for _, email := range m.config.AdminEmails {
		emails = append(emails, strings.ToLower(strings.TrimSpace(email)))
	}
	return emails
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func settingInt(settings map[string]string, key string, fallback int) int {
	if v, ok := settings[key]; ok {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}
