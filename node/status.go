package node

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"heckel.io/hostit/cluster"
	"heckel.io/hostit/node/link"
	"heckel.io/hostit/store"
)

// Status is what `hostit node status` prints: the daemon's identity, whether
// its control link is up, and the apps control has placed on this host. It is
// the NODE's own view -- readable on a worker host with no control anywhere
// near it, which is exactly where an operator most wants to look.
type Status struct {
	NodeID     string      `json:"node_id"`
	Version    string      `json:"version"`
	ControlURL string      `json:"control_url"`
	Connected  bool        `json:"connected"`
	Apps       []StatusApp `json:"apps"`
}

// StatusApp is one mirrored app row: what control says this node hosts.
type StatusApp struct {
	Name string `json:"name"`
	UID  int    `json:"uid"`
	Port int    `json:"port"`
}

// nodeStatus assembles the Status from the pieces the daemon holds.
func nodeStatus(conf *Config, st *store.Store, link *link.ControlLink, version string) (*Status, error) {
	apps, err := st.Apps()
	if err != nil {
		return nil, err
	}
	statusApps := make([]StatusApp, 0, len(apps))
	for _, a := range apps {
		statusApps = append(statusApps, StatusApp{Name: a.Name, UID: a.UID, Port: a.Port})
	}
	return &Status{
		NodeID:     conf.NodeID,
		Version:    version,
		ControlURL: conf.ControlURL,
		Connected:  link.Client() != nil,
		Apps:       statusApps,
	}, nil
}

// ServeStatusSocket serves the node's status on a root-only unix socket, for
// `hostit node status`. cluster.ListenSocket's 0600 is the whole story: only
// root may ask, and nothing served here mutates anything.
func ServeStatusSocket(path string, conf *Config, st *store.Store, link *link.ControlLink, version string) (io.Closer, error) {
	listener, err := cluster.ListenSocket(path)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		s, err := nodeStatus(conf, st, link, version)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s)
	})
	server := &http.Server{Handler: mux}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Warn("Status socket server failed", "socket", path, "error", err)
		}
	}()
	return listener, nil
}
