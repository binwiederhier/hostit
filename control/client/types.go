package client

import (
	"time"
)

// SSHInfo describes how to SSH into an app
type SSHInfo struct {
	User    string `json:"user"`
	Host    string `json:"host"`
	Command string `json:"command"`
}

// App mirrors the server's app response
type App struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Port      int       `json:"port"`
	CreatedAt time.Time `json:"created_at"`
	SSH       SSHInfo   `json:"ssh"`
	// Private restricts the app's URL to its owner, collaborators and admins.
	Private bool `json:"private"`
}

// messageResponse and outputResponse mirror the server's lifecycle and log replies
type messageResponse struct {
	Message string `json:"message"`
}

type outputResponse struct {
	Output string `json:"output"`
}

// runRequest asks the server to run one command in the app's container
type runRequest struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

// RunResult is what that command left behind
type RunResult struct {
	Output    string `json:"output"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated"`
	TimedOut  bool   `json:"timed_out"`
}

// errorResponse mirrors the server's error body
type errorResponse struct {
	Error string `json:"error"`
}

// createAppRequest mirrors the server's POST /api/apps body
type createAppRequest struct {
	Name    string   `json:"name"`
	SSHKeys []string `json:"ssh_keys,omitempty"`
	Private bool     `json:"private,omitempty"`
}

// forkAppRequest mirrors the server's POST /api/apps/{name}/fork body
type forkAppRequest struct {
	NewName    string `json:"new_name"`
	SnapshotID string `json:"snapshot_id,omitempty"`
}

// addDomainRequest mirrors the server's POST /api/apps/{name}/domains body
type addDomainRequest struct {
	Domain string `json:"domain"`
}

// DNSRecord is one DNS record the owner must create for a custom domain
type DNSRecord struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Value string `json:"value"`
	Note  string `json:"note"`
}

// DomainInfo is a custom domain as returned by the API
type DomainInfo struct {
	Domain    string      `json:"domain"`
	Status    string      `json:"status"`
	LastError string      `json:"last_error,omitempty"`
	DNS       []DNSRecord `json:"dns"`
}

// setKeysRequest mirrors the server's PUT /api/apps/{name}/keys body
type setKeysRequest struct {
	SSHKeys []string `json:"ssh_keys"`
}

// setVisibilityRequest mirrors the server's PUT /api/apps/{name}/visibility body
type setVisibilityRequest struct {
	Private bool `json:"private"`
}
