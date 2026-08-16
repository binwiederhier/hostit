package appctl

import (
	"time"
)

// SelfInfo is what the daemon's unix socket /v1/self endpoint returns about the
// calling app; field names match the server's app response
type SelfInfo struct {
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	Port      int       `json:"port"`
	CreatedAt time.Time `json:"created_at"`
}

// messageResponse mirrors the daemon's lifecycle action responses
type messageResponse struct {
	Message string `json:"message"`
}

// outputResponse mirrors the daemon's status/log responses
type outputResponse struct {
	Output string `json:"output"`
}

// errorResponse mirrors the daemon's error responses
type errorResponse struct {
	Error string `json:"error"`
}

// toolResponse mirrors the daemon's /v1/self/tool result: one tool call's
// model-facing output and whether the tool reported an error
type toolResponse struct {
	Output  string `json:"output"`
	IsError bool   `json:"is_error"`
}
