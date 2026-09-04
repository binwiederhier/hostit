package store

import (
	"time"
)

const (
	// HostLocal is the name of the colocated node (the one on control's own
	// host); app.Host and the node table key on node names.
	HostLocal = "local"
)

// Node is one app-running machine, registered by `node add` (which also mints
// its mTLS certificate). A node authenticates purely by that certificate's CN
// matching this row's name; the row is the membership switch `node remove`
// flips off.
// Proxy is a registered data-plane proxy. It has no address: a proxy dials
// control, never the other way round, so control never needs to reach it.
type Proxy struct {
	Name string `json:"name"`
	// Stats is the raw JSON the proxy last reported about its machine (see
	// hoststats); empty until it reports. Kept as a blob here so the registry
	// does not grow a column per metric.
	Stats string `json:"stats,omitempty"`
	// Version is the build the proxy reported, and Routes how many routes it was
	// serving, both from its last heartbeat.
	Version string `json:"version"`
	Routes  int    `json:"routes"`
	// RegisteredAt is when the proxy was added; LastSeen is its last connect,
	// which is how an operator tells a configured proxy from a serving one.
	RegisteredAt time.Time `json:"registered_at"`
	LastSeen     time.Time `json:"last_seen"`
}

type Node struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	// Stats is the raw JSON the node last reported about its machine; see Proxy.
	Stats string `json:"stats,omitempty"`
	// JoinedAt is a leftover of the retired join-token flow (kept for schema
	// compatibility); liveness is LastSeen.
	JoinedAt time.Time `json:"joined_at"`
	// LastSeen is the last connect/heartbeat, for liveness display and placement.
	LastSeen time.Time `json:"last_seen"`
	// SSHHost is the hostname clients use to SSH to apps on this node, reported
	// by the node itself (it knows its own reachable address). Empty falls back
	// to control's base domain -- correct for the colocated node, wrong for a
	// remote node, which is why a remote node sets it.
	SSHHost string `json:"ssh_host,omitempty"`
	// HostKey is this node's sshd public host key (authorized-keys line format),
	// reported by the node over the authenticated cluster tunnel. Used to write
	// the relay gateway's known_hosts so the frontend->node hop is verified.
	HostKey string `json:"host_key,omitempty"`
	// Version is the build the node reported on its last heartbeat.
	Version string `json:"version,omitempty"`
}

// Role is a user's permission level
type Role string

const (
	// RoleAdmin may manage all users, apps and global settings
	RoleAdmin = Role("admin")
	// RoleUser may manage only their own apps, keys and tokens
	RoleUser = Role("user")
	// RoleViewer may only OPEN apps shared with them; they cannot create or
	// manage apps of their own (their effective app limit is 0). Meant for people
	// invited purely to view a private app.
	RoleViewer = Role("viewer")
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
	CreatedAt time.Time `json:"created_at"`
	// ImageTag pins the app to the workspace image it was built with; empty until
	// the daemon backfills it (apps created before image pinning).
	ImageTag string `json:"image_tag"`
	// UID is the base of the app's contiguous uid block on its hosting node,
	// allocated there and recorded here so the control plane can reason about
	// the app without asking the node. 0 until the daemon backfills it (apps
	// created before uid recording).
	UID int `json:"uid"`
	// PoweredOff records a deliberate poweroff (cleared by poweron). Recorded
	// here, not inferred from systemd, where a never-enabled unit also reads
	// "disabled" and a fresh app would look powered off.
	PoweredOff bool `json:"powered_off"`
	// Archived is a stronger, deliberate shelving: the app cannot be powered on,
	// deployed to, or started by a login, and it stops taking new snapshots. It
	// is not powered_off, which an owner flips freely -- an archived app has to
	// be brought back before it can run at all.
	Archived bool `json:"archived"`
	// SoftDeletedAt is when the owner deleted the app. A soft-deleted app is gone
	// from the owner's view -- like an archived app, but hidden entirely -- and a
	// background sweep hard-deletes it once soft-delete-duration has passed. Zero
	// means the app is live.
	SoftDeletedAt time.Time `json:"soft_deleted_at,omitempty"`
	// Listed marks a PUBLIC app the owner has chosen to show on the instance's
	// public app gallery ("Explore"). Off by default; only meaningful when the
	// app is public and the instance has the gallery enabled (app-listing).
	Listed bool `json:"listed,omitempty"`
	// Private restricts who may reach the app over HTTP: its owner, its
	// collaborators and admins, rather than anyone with the URL. Public is the
	// default, which is what every app predating this flag already was.
	Private bool `json:"private"`
	// Per-app resource limit OVERRIDES, admin-set. 0 means no override:
	// memory/disk fall back to the owner's defaults, CPU stays uncapped.
	// (DiskMB above is USAGE, written by the node's usage callback.)
	MemoryLimitMB int `json:"memory_limit_mb"`
	DiskLimitMB   int `json:"disk_limit_mb"`
	CPUMilli      int `json:"cpu_milli"`
	// Tabs is the owner's per-app override of which app-detail tabs show, as a
	// CSV of tab keys (assistant,files,terminal,logs). Empty means no override:
	// each viewer's own profile default applies.
	Tabs string `json:"tabs"`
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
	ID       string `json:"id"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Role     Role   `json:"role"`
	Status   Status `json:"status"`
	AppLimit *int   `json:"app_limit"`
	MemoryMB *int   `json:"memory_mb"`
	DiskMB   *int   `json:"disk_mb"`
	// The user's resource POOLS: the budget all their apps' effective limits
	// must fit inside. nil derives app_limit x the per-app default.
	MemoryPoolMB *int      `json:"memory_pool_mb"`
	DiskPoolMB   *int      `json:"disk_pool_mb"`
	CreatedAt    time.Time `json:"created_at"`
	// Self-service profile fields, set by the user (not the admin). TechLevel is
	// "" until the welcome modal is answered ("novice"|"intermediate"|"expert");
	// AssistantPrompt is appended to the assistant's system prompt; DefaultTabs
	// is the CSV of app-detail tabs they want shown by default (empty = built-in
	// default); Onboarded records that they have seen the welcome modal.
	TechLevel       string `json:"tech_level"`
	AssistantPrompt string `json:"assistant_prompt"`
	DefaultTabs     string `json:"default_tabs"`
	Onboarded       bool   `json:"onboarded"`
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
