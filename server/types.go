package server

import (
	"encoding/json"
	"time"

	"heckel.io/hostit/app"
	"heckel.io/hostit/store"
	"heckel.io/hostit/user"
)

// apiCreateAppRequest is the body of POST /v1/apps
type apiCreateAppRequest struct {
	Name    string   `json:"name"`
	SSHKeys []string `json:"ssh_keys"`
}

// apiSetKeysRequest is the body of PUT /v1/apps/{name}/keys
type apiSetKeysRequest struct {
	SSHKeys []string `json:"ssh_keys"`
}

// apiSSHInfo describes how to SSH into an app
type apiSSHInfo struct {
	User    string `json:"user"`
	Host    string `json:"host"`
	Command string `json:"command"`
}

// apiAppResponse is returned for all app-related endpoints; PrivateKey/PublicKey
// are only set on creation when hostit generated a key pair for the caller
type apiAppResponse struct {
	Name        string     `json:"name"`
	URL         string     `json:"url"`
	Port        int        `json:"port"`
	DiskMB      int        `json:"disk_mb"`
	DiskLimit   int        `json:"disk_limit_mb"`
	MemoryMB    int        `json:"memory_mb"`
	MemoryLimit int        `json:"memory_limit_mb"`
	Running     bool       `json:"running"`
	OverQuota   bool       `json:"over_quota"`
	OwnerEmail  string     `json:"owner_email,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	SSH         apiSSHInfo `json:"ssh"`
	AgentToken  string     `json:"agent_token,omitempty"` // App-scoped; shown to the owner so the page can always render the prompt
	PrivateKey  string     `json:"private_key,omitempty"`
	PublicKey   string     `json:"public_key,omitempty"`
}

// apiHealthResponse is returned by GET /v1/health
type apiHealthResponse struct {
	Healthy bool `json:"healthy"`
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

// apiUsage is a user's current consumption, shown next to their limits
type apiUsage struct {
	Apps   int `json:"apps"`
	DiskMB int `json:"disk_mb"`
}

// apiAccountResponse is GET /v1/account: who the caller is and what they may use
type apiAccountResponse struct {
	Email  string       `json:"email"`
	Name   string       `json:"name"`
	Role   store.Role   `json:"role"`
	Status store.Status `json:"status"`
	Limits *user.Limits `json:"limits,omitempty"`
	Usage  *apiUsage    `json:"usage,omitempty"`
}

// apiAddKeyRequest is the body of POST /v1/account/keys
type apiAddKeyRequest struct {
	Label string `json:"label"`
	Key   string `json:"key"`
}

// apiAddTokenRequest is the body of POST /v1/account/tokens; an app_name limits
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
	ID        string       `json:"id"`
	Email     string       `json:"email"`
	Name      string       `json:"name"`
	Role      store.Role   `json:"role"`
	Status    store.Status `json:"status"`
	AppLimit  *int         `json:"app_limit"`
	MemoryMB  *int         `json:"memory_mb"`
	DiskMB    *int         `json:"disk_mb"`
	AppCount  int          `json:"app_count"`
	CreatedAt time.Time    `json:"created_at"`
}

// apiUpdateUserRequest is the body of PATCH /v1/users/{id}. The *Set fields
// distinguish "absent" from "explicitly null" for the nullable limit overrides.
type apiUpdateUserRequest struct {
	Role     *store.Role   `json:"role"`
	Status   *store.Status `json:"status"`
	AppLimit *int          `json:"app_limit"`
	MemoryMB *int          `json:"memory_mb"`
	DiskMB   *int          `json:"disk_mb"`

	AppLimitSet bool `json:"-"`
	MemoryMBSet bool `json:"-"`
	DiskMBSet   bool `json:"-"`
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
	return nil
}

// apiSettingsResponse is the global default limits
type apiSettingsResponse struct {
	DefaultAppLimit int `json:"default_app_limit"`
	DefaultMemoryMB int `json:"default_memory_mb"`
	DefaultDiskMB   int `json:"default_disk_mb"`
}

// apiUpdateSettingsRequest is the body of PATCH /v1/settings
type apiUpdateSettingsRequest struct {
	DefaultAppLimit *int `json:"default_app_limit"`
	DefaultMemoryMB *int `json:"default_memory_mb"`
	DefaultDiskMB   *int `json:"default_disk_mb"`
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
	HostitYml      string             `json:"hostit_yml"`
	Runtimes       string             `json:"runtimes"`
	SuggestedStack string             `json:"suggested_stack"`
	Endpoints      []apiAgentEndpoint `json:"endpoints"`
	Notes          []string           `json:"notes"`
}

// apiAgentAppResponse is GET /api/{app}/info
type apiAgentAppResponse struct {
	Name      string          `json:"name"`
	URL       string          `json:"url"`
	Running   bool            `json:"running"`
	DiskMB    int             `json:"disk_mb"`
	OverQuota bool            `json:"over_quota"`
	Readme    string          `json:"readme"`
	HostitYml string          `json:"hostit_yml"`
	Files     []*app.FileInfo `json:"files"`
	SSH       apiSSHInfo      `json:"ssh"`
	Hint      string          `json:"hint"`
	// Guide is the full instruction set, inlined so an agent pointed at this one
	// URL needs nothing else
	Guide *apiAgentInfoResponse `json:"guide"`
}

// apiReadmeRequest is the body of PUT /api/{app}/readme
type apiReadmeRequest struct {
	Readme string `json:"readme"`
}

// apiFilesWrittenResponse lists what a tar upload wrote
type apiFilesWrittenResponse struct {
	Written []string `json:"written"`
}
