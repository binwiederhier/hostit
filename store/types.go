package store

import (
	"time"
)

const (
	// HostLocal is the runner host for apps on this machine; other values are reserved
	// for the future multi-runner setup (see plans/260803-hostit.md).
	HostLocal = "local"
)

// App is a registered app: one Unix user, one loopback port, one subdomain
type App struct {
	Name      string    `json:"name"`
	Port      int       `json:"port"`
	Host      string    `json:"host"`
	CreatedAt time.Time `json:"created_at"`
}
