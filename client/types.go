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

// errorResponse mirrors the server's error body
type errorResponse struct {
	Error string `json:"error"`
}

// createAppRequest mirrors the server's POST /v1/apps body
type createAppRequest struct {
	Name    string   `json:"name"`
	SSHKeys []string `json:"ssh_keys,omitempty"`
}

// setKeysRequest mirrors the server's PUT /v1/apps/{name}/keys body
type setKeysRequest struct {
	SSHKeys []string `json:"ssh_keys"`
}
