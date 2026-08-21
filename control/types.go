package control

import (
	"encoding/json"
	"time"

	"heckel.io/hostit/store"
	"heckel.io/hostit/user"
)

// apiCreateAppRequest is the body of POST /api/apps
type apiCreateAppRequest struct {
	Name    string   `json:"name"`
	SSHKeys []string `json:"ssh_keys"`
}

// apiCollaboratorResponse is one collaborator row: enough for a settings list.
type apiCollaboratorResponse struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

// apiAddCollaboratorRequest is the body of POST /api/apps/{name}/collaborators.
type apiAddCollaboratorRequest struct {
	Email string `json:"email"`
}

// apiSetKeysRequest is the body of PUT /api/apps/{name}/keys
type apiSetKeysRequest struct {
	SSHKeys []string `json:"ssh_keys"`
}

// apiSnapshotConfig is an app's snapshot settings as hostit.yml holds them. An
// empty interval means the app has not chosen one and takes the default, which
// is why the default is reported alongside rather than substituted -- the UI
// has to show the difference between "3h because I said so" and "3h because
// that is the default".
type apiSnapshotConfig struct {
	Interval        string `json:"interval"`
	Pre             string `json:"pre"`
	Post            string `json:"post"`
	DefaultInterval string `json:"default_interval,omitempty"`
}

// apiSetDescriptionRequest is the body of PUT /api/apps/{name}/description
type apiSetDescriptionRequest struct {
	Description string `json:"description"`
}

// apiRenameAppRequest is the body of POST /api/apps/{name}/rename
type apiRenameAppRequest struct {
	NewName string `json:"new_name"`
}

// apiForkAppRequest is the body of POST /api/apps/{name}/fork: the name of the new
// app, and optionally a snapshot to seed it from (empty means the current home)
type apiForkAppRequest struct {
	NewName    string `json:"new_name"`
	SnapshotID string `json:"snapshot_id,omitempty"`
}

// apiSSHInfo describes how to SSH into an app
type apiSSHInfo struct {
	User    string `json:"user"`
	Host    string `json:"host"`
	Command string `json:"command"`
}

// apiAppResponse is returned for all app-related endpoints
type apiAppResponse struct {
	// IsOwner says whether the CALLER owns the app (or is an admin): a
	// collaborator's dashboard shows the app but not the ownership acts.
	IsOwner     bool   `json:"is_owner"`
	ID          string `json:"id"` // Stable opaque id; the UI derives an app's avatar colour from it
	Name        string `json:"name"`
	URL         string `json:"url"`
	Port        int    `json:"port"`
	DiskMB      int    `json:"disk_mb"`
	DiskLimit   int    `json:"disk_limit_mb"`
	MemoryMB    int    `json:"memory_mb"`
	MemoryLimit int    `json:"memory_limit_mb"`
	CPUPercent  int    `json:"cpu_percent"` // Live container CPU use in whole percent
	// CPUMilli is the EFFECTIVE CPU cap in millicores (0 = uncapped), like
	// MemoryLimit/DiskLimit are the effective caps; LimitOverrides carries the
	// admin-set per-app overrides so the UI can tell inherited from set.
	CPUMilli       int               `json:"cpu_milli"`
	LimitOverrides apiLimitOverrides `json:"limit_overrides"`

	Running          bool   `json:"running"`        // The app's container is up
	AppRunning       bool   `json:"app_running"`    // The run: command inside it is up
	AppState         string `json:"app_state"`      // Agent breadcrumb: running/crashed/failed/stopped/idle, "" if the container is down
	StartedAt        int64  `json:"started_at"`     // Unix seconds the container last started
	AppStartedAt     int64  `json:"app_started_at"` // Unix millis the run: process last changed state
	OwnerEmail       string `json:"owner_email,omitempty"`
	OwnerName        string `json:"owner_name,omitempty"`
	SnapshotsEnabled bool   `json:"snapshots_enabled"` // true when the host supports snapshots (btrfs)
	PreviewMode      string `json:"preview_mode"`      // How the UI previews the app: "live", "screenshot" or "off"
	AssistantEnabled bool   `json:"assistant_enabled"` // true when an Anthropic API key is configured
	// Description is the app's own one-liner from hostit.yml, kept current by
	// whoever builds it; empty means the app is still a stub
	Description string     `json:"description"`
	CreatedAt   time.Time  `json:"created_at"`
	SSH         apiSSHInfo `json:"ssh"`
	// Snapshot is what hostit.yml asks for, so Settings can show and edit it
	Snapshot apiSnapshotConfig `json:"snapshot"`
	// Archived is the app shelved: powered off, refusing to run, and taking no
	// new snapshots until it is brought back
	Archived bool `json:"archived"`
	// CustomDomain is the first verified (active) custom domain, empty if none; the
	// web app prefers it over the default subdomain for links and previews.
	CustomDomain string `json:"custom_domain,omitempty"`
	AgentToken   string `json:"agent_token,omitempty"` // App-scoped; shown to the owner so the page can always render the prompt
}

// apiHealthResponse is returned by GET /api/health
type apiHealthResponse struct {
	Healthy bool `json:"healthy"`
}

// apiEventResponse is one activity-log entry in GET /api/apps/{name}/events
type apiEventResponse struct {
	Time   time.Time `json:"time"`
	Actor  string    `json:"actor,omitempty"` // email that did it; empty for the system
	Level  string    `json:"level"`           // "info" | "error"
	Action string    `json:"action"`
	Detail string    `json:"detail"`
}

// apiErrorResponse is returned for all error conditions
type apiErrorResponse struct {
	Error string `json:"error"`
}

// apiMessageResponse carries a human-readable result of a lifecycle action
type apiMessageResponse struct {
	Message string `json:"message"`
}

// apiOutputResponse carries command/log output for the CLI to print
type apiOutputResponse struct {
	Output string `json:"output"`
}

// apiToolResponse is the result of one app-scoped tool call over the self
// socket: the model-facing output, and whether the tool reported an error (which
// the model reads and adapts to, exactly as a failed shell command)
type apiToolResponse struct {
	Output  string `json:"output"`
	IsError bool   `json:"is_error"`
}

// apiUsage is a user's current consumption, shown next to their limits
type apiUsage struct {
	Apps   int `json:"apps"`
	DiskMB int `json:"disk_mb"`
	// The pool allocation: the sum of this user's apps' effective limits, which
	// is what their pools bound (usage above is what is USED; this is RESERVED).
	PoolMemoryMB int `json:"pool_memory_mb"`
	PoolDiskMB   int `json:"pool_disk_mb"`
}

// apiAccountResponse is GET /api/account: who the caller is and what they may use
type apiAccountResponse struct {
	Email  string       `json:"email"`
	Name   string       `json:"name"`
	Role   store.Role   `json:"role"`
	Status store.Status `json:"status"`
	Limits *user.Limits `json:"limits,omitempty"`
	Usage  *apiUsage    `json:"usage,omitempty"`
}

// apiAddKeyRequest is the body of POST /api/account/keys
type apiAddKeyRequest struct {
	Label string `json:"label"`
	Key   string `json:"key"`
}

// apiAddTokenRequest is the body of POST /api/account/tokens; an app_name limits
// the token to that one app (what the per-app page hands to an agent)
type apiAddTokenRequest struct {
	Label   string `json:"label"`
	AppName string `json:"app_name"`
}

// apiTokenResponse is a created token; Token is set only on creation
type apiTokenResponse struct {
	ID        string    `json:"id"`
	Prefix    string    `json:"prefix"`
	Label     string    `json:"label"`
	AppName   string    `json:"app_name,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	Token     string    `json:"token,omitempty"`
}

// apiUserResponse is a user as seen by admins
type apiUserResponse struct {
	ID       string       `json:"id"`
	Email    string       `json:"email"`
	Name     string       `json:"name"`
	Role     store.Role   `json:"role"`
	Status   store.Status `json:"status"`
	AppLimit *int         `json:"app_limit"`
	MemoryMB *int         `json:"memory_mb"`
	DiskMB   *int         `json:"disk_mb"`
	// The pools (null = derived); the admin page edits these.
	MemoryPoolMB *int      `json:"memory_pool_mb"`
	DiskPoolMB   *int      `json:"disk_pool_mb"`
	AppCount     int       `json:"app_count"`
	CreatedAt    time.Time `json:"created_at"`
	// Built-in assistant usage summed across the user's apps: total tokens and the
	// dollar cost. Only the built-in assistant, never a tenant's own agent.
	AssistantTokens  int64   `json:"assistant_tokens"`
	AssistantCostUSD float64 `json:"assistant_cost_usd"`

	// The user's effective assistant permissions: may they use External Claude, and
	// which API models. HasOverride is true when these are an explicit per-user
	// setting rather than the inherited global default.
}

// apiInviteUserRequest is the body of POST /api/users: an admin handing out
// access before the person has ever signed in. An empty role means "user".
type apiInviteUserRequest struct {
	Email string     `json:"email"`
	Role  store.Role `json:"role"`
}

// apiAddDomainRequest is the body of POST /api/domains
type apiAddDomainRequest struct {
	Domain string `json:"domain"`
}

// apiDomainResponse is an email domain whose users skip the approval queue
type apiDomainResponse struct {
	Domain    string    `json:"domain"`
	CreatedAt time.Time `json:"created_at"`
}

// apiUpdateUserRequest is the body of PATCH /api/users/{id}. The *Set fields
// distinguish "absent" from "explicitly null" for the nullable limit overrides.
type apiUpdateUserRequest struct {
	Role     *store.Role   `json:"role"`
	Status   *store.Status `json:"status"`
	AppLimit *int          `json:"app_limit"`
	MemoryMB *int          `json:"memory_mb"`
	DiskMB   *int          `json:"disk_mb"`
	// The pools an owner's apps draw from (null = derive app_limit x default).
	MemoryPoolMB *int `json:"memory_pool_mb"`
	DiskPoolMB   *int `json:"disk_pool_mb"`

	AppLimitSet     bool `json:"-"`
	MemoryMBSet     bool `json:"-"`
	DiskMBSet       bool `json:"-"`
	MemoryPoolMBSet bool `json:"-"`
	DiskPoolMBSet   bool `json:"-"`
}

// UnmarshalJSON records which limit fields were present in the request body
func (r *apiUpdateUserRequest) UnmarshalJSON(b []byte) error {
	type alias apiUpdateUserRequest // Avoid recursing into this method
	if err := json.Unmarshal(b, (*alias)(r)); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	_, r.AppLimitSet = raw["app_limit"]
	_, r.MemoryMBSet = raw["memory_mb"]
	_, r.DiskMBSet = raw["disk_mb"]
	_, r.MemoryPoolMBSet = raw["memory_pool_mb"]
	_, r.DiskPoolMBSet = raw["disk_pool_mb"]
	return nil
}

// apiSettingsResponse is the global default limits
type apiSettingsResponse struct {
	DefaultAppLimit int `json:"default_app_limit"`
	DefaultMemoryMB int `json:"default_memory_mb"`
	DefaultDiskMB   int `json:"default_disk_mb"`
	// The default POOLS a user gets; 0 = derive app_limit x per-app default.
	DefaultMemoryPoolMB int `json:"default_memory_pool_mb"`
	DefaultDiskPoolMB   int `json:"default_disk_pool_mb"`
}

// apiUpdateSettingsRequest is the body of PATCH /api/settings
type apiUpdateSettingsRequest struct {
	DefaultAppLimit     *int `json:"default_app_limit"`
	DefaultMemoryMB     *int `json:"default_memory_mb"`
	DefaultDiskMB       *int `json:"default_disk_mb"`
	DefaultMemoryPoolMB *int `json:"default_memory_pool_mb"`
	DefaultDiskPoolMB   *int `json:"default_disk_pool_mb"`
}

// apiAgentEndpoint documents one endpoint in the agent-facing API index
type apiAgentEndpoint struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	What   string `json:"what"`
}

// apiAgentInfoResponse is GET /api/info: everything an agent needs to work with
// hostit without any prior knowledge
type apiAgentInfoResponse struct {
	Platform       string             `json:"platform"`
	BaseURL        string             `json:"base_url"`
	WhatIsThis     string             `json:"what_is_this"`
	Auth           string             `json:"auth"`
	Workflow       []string           `json:"workflow"`
	Layout         string             `json:"layout"`
	HostitYml      string             `json:"hostit_yml"`
	Runtimes       string             `json:"runtimes"`
	SuggestedStack string             `json:"suggested_stack"`
	Endpoints      []apiAgentEndpoint `json:"endpoints"`
	Notes          []string           `json:"notes"`
}

// apiAgentAppResponse is GET /api/apps/{app}/info
type apiAgentAppResponse struct {
	Name      string     `json:"name"`
	URL       string     `json:"url"`
	Running   bool       `json:"running"`
	DiskMB    int        `json:"disk_mb"`
	Readme    string     `json:"readme"`
	HostitYml string     `json:"hostit_yml"`
	Files     *Listing   `json:"files"`
	SSH       apiSSHInfo `json:"ssh"`
	Hint      string     `json:"hint"`
	// Guide is the full instruction set, inlined so an agent pointed at this one
	// URL needs nothing else
	Guide *apiAgentInfoResponse `json:"guide"`
}

// apiAgentAssistantResponse is GET /api/apps/{app}/assistant/transcript: the
// built-in assistant's session for the app, rendered as markdown so an external
// agent can pick up with the full history of what was already tried
type apiAgentAssistantResponse struct {
	Enabled    bool   `json:"enabled"` // false when no assistant is configured on this server
	Running    bool   `json:"running"` // a turn is in progress right now
	Messages   int    `json:"messages"`
	Transcript string `json:"transcript"`
}

// apiMoveRequest is the body of POST /api/apps/{app}/move: rename/move a file
type apiMoveRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// apiMkdirRequest is the body of POST /api/apps/{app}/mkdir: create an empty dir
type apiMkdirRequest struct {
	Path string `json:"path"`
}

// apiRunRequest is the body of POST /api/apps/{app}/run
type apiRunRequest struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds"` // 0 = the default; capped by the daemon
}

// apiRunResponse is what the command left behind
type apiRunResponse struct {
	Output    string `json:"output"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated"`
	TimedOut  bool   `json:"timed_out"`
}

// apiReadmeRequest is the body of PUT /api/apps/{app}/readme
type apiReadmeRequest struct {
	Readme string `json:"readme"`
}

// apiFilesWrittenResponse lists what a tar upload wrote
type apiFilesWrittenResponse struct {
	Written []string `json:"written"`
}
