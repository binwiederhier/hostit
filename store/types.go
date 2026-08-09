package store

import (
	"time"
)

const (
	// HostLocal is the runner host for apps on this machine; other values are
	// reserved for the future multi-node setup (see TODO.md)
	HostLocal = "local"
)

// Role is a user's permission level
type Role string

const (
	// RoleAdmin may manage all users, apps and global settings
	RoleAdmin = Role("admin")
	// RoleUser may manage only their own apps, keys and tokens
	RoleUser = Role("user")
)

// Status is where a user stands in the approval workflow
type Status string

const (
	// StatusPending is a Google-authenticated user awaiting admin approval
	StatusPending = Status("pending")
	// StatusActive is an approved user
	StatusActive = Status("active")
	// StatusDenied is a rejected or suspended user
	StatusDenied = Status("denied")
)

// App is a registered app: one Unix user, one container, one loopback port, one subdomain
type App struct {
	// ID is the app's stable, opaque identity: durable resources (home dir,
	// container, snapshots, FKs) key on it, so a rename touches only Name. Empty
	// until the daemon backfills it (apps created before app ids).
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Port      int       `json:"port"`
	Host      string    `json:"host"`
	OwnerID   string    `json:"owner_id"`
	DiskMB    int       `json:"disk_mb"`
	OverQuota bool      `json:"over_quota"`
	CreatedAt time.Time `json:"created_at"`
	// ImageTag pins the app to the workspace image it was built with; empty until
	// the daemon backfills it (apps created before image pinning).
	ImageTag string `json:"image_tag"`
}

// Snapshot is one point-in-time btrfs snapshot of an app's home. Auto records how
// it was taken (automatically before a deploy/turn and hourly, or a manual labelled
// save); retention thins all of them, so none lives forever.
type Snapshot struct {
	ID        string    `json:"id"`
	AppName   string    `json:"app_name"`
	Label     string    `json:"label,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Auto      bool      `json:"auto"`
}

// User is a person who logs in via Google; limits are nil when the global
// default applies
type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Role      Role      `json:"role"`
	Status    Status    `json:"status"`
	AppLimit  *int      `json:"app_limit"`
	MemoryMB  *int      `json:"memory_mb"`
	DiskMB    *int      `json:"disk_mb"`
	CreatedAt time.Time `json:"created_at"`
}

// Token is a per-user API credential; only its hash is stored
type Token struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Hash      string    `json:"-"`
	Prefix    string    `json:"prefix"`
	Label     string    `json:"label"`
	AppName   string    `json:"app_name"` // Empty = account-wide; otherwise the only app this token may touch
	Secret    string    `json:"-"`        // Only set for app tokens, so their page can show them again
	CreatedAt time.Time `json:"created_at"`
	// LastUsed is nil until the token is first used, so clients can say "never"
	// rather than rendering the zero time as a date in year 1
	LastUsed *time.Time `json:"last_used"`
}

// AllowedDomain is an email domain whose users skip the approval queue: anyone
// signing in with an address in it is active immediately
type AllowedDomain struct {
	Domain    string    `json:"domain"`
	CreatedAt time.Time `json:"created_at"`
}

// DomainStatus is a custom domain's lifecycle state
type DomainStatus string

const (
	// DomainPending: added, but no certificate yet, so it does not route
	DomainPending = DomainStatus("pending")
	// DomainActive: certificate obtained; the domain routes to its app
	DomainActive = DomainStatus("active")
	// DomainError: the last certificate attempt failed (see LastError)
	DomainError = DomainStatus("error")
)

// Domain is a custom hostname that routes to an app, on top of the app's
// <app>.<base-domain> subdomain. The primary key enforces that a domain maps to
// exactly one app.
type Domain struct {
	Domain    string       `json:"domain"`
	AppName   string       `json:"app_name"`
	Status    DomainStatus `json:"status"`
	LastError string       `json:"last_error,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
	ActiveAt  *time.Time   `json:"active_at,omitempty"`
}

// UserKey is an SSH public key from a user's profile; it grants access to all
// apps that user owns
type UserKey struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Label     string    `json:"label"`
	Key       string    `json:"key"`
	CreatedAt time.Time `json:"created_at"`
}
