package server

import (
	"time"
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
	Name       string     `json:"name"`
	URL        string     `json:"url"`
	Port       int        `json:"port"`
	CreatedAt  time.Time  `json:"created_at"`
	SSH        apiSSHInfo `json:"ssh"`
	PrivateKey string     `json:"private_key,omitempty"`
	PublicKey  string     `json:"public_key,omitempty"`
}

// apiHealthResponse is returned by GET /v1/health
type apiHealthResponse struct {
	Healthy bool `json:"healthy"`
}

// apiErrorResponse is returned for all error conditions
type apiErrorResponse struct {
	Error string `json:"error"`
}
