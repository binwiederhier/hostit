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
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Port      int       `json:"port"`
	CreatedAt time.Time `json:"created_at"`
	SSH       SSHInfo   `json:"ssh"`
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
}

// forkAppRequest mirrors the server's POST /api/apps/{name}/fork body
type forkAppRequest struct {
	NewName string `json:"new_name"`
}

// setKeysRequest mirrors the server's PUT /api/apps/{name}/keys body
type setKeysRequest struct {
	SSHKeys []string `json:"ssh_keys"`
}
