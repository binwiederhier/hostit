package control

import (
	"encoding/json"
	"sort"
	"time"

	"heckel.io/hostit/node"
	"heckel.io/hostit/store"
	"heckel.io/hostit/system/stats"
)

// The cluster's status, assembled from the registry alone: `hostit-control
// status` runs as a separate process with no daemon to ask, and the admin view
// reads the same shape over the API, so both see one answer built one way.
//
// Everything here is a recorded fact, never a live measurement. A node's app
// count and an app's disk usage are what the daemon last wrote down, which is
// what makes this readable from another process at all -- and why nothing here
// claims an app is "running": that lives in the daemon's memory, and guessing
// it from the registry would be a confident lie.

const (
	// staleAfter is how long without a heartbeat before a member is reported
	// stale. Nodes are polled every couple of seconds and proxies every 30, so
	// two minutes is well clear of a slow pass while still catching a member
	// that died quietly.
	staleAfter = 2 * time.Minute
)

// Status is the whole cluster at a glance.
type Status struct {
	// Control is the control plane's OWN machine, measured live rather than
	// reported: it is the one member that does not dial in, and its box
	// filling up is what stops the registry.
	Control  *MemberStatus   `json:"control"`
	Nodes    []*MemberStatus `json:"nodes"`
	Proxies  []*MemberStatus `json:"proxies"`
	Apps     *AppTotals      `json:"apps"`
	People   *PeopleTotals   `json:"people"`
	Snapshot time.Time       `json:"as_of"`
}

// MemberStatus is one node or proxy. Address is empty for a proxy (control
// never dials one); Version and Routes are empty for a node, which reports
// neither.
type MemberStatus struct {
	Name     string    `json:"name"`
	Address  string    `json:"address,omitempty"`
	Version  string    `json:"version,omitempty"`
	Routes   int       `json:"routes,omitempty"`
	Apps     int       `json:"apps"`
	LastSeen time.Time `json:"last_seen"`
	// Stale is true when the last heartbeat is old enough to be worth looking
	// at, and when a member has never reported at all.
	Stale bool `json:"stale"`
	// Stats is what the member last reported about its own machine. Zero
	// throughout when it has never reported (stats.Stats.Known()).
	Stats stats.Stats `json:"stats"`
}

// AppTotals counts the apps and what they are using.
type AppTotals struct {
	Total      int `json:"total"`
	PoweredOff int `json:"powered_off"`
	Unplaced   int `json:"unplaced"` // hosted by a node that is not registered
	DiskUsedMB int `json:"disk_used_mb"`
	Snapshots  int `json:"snapshots"`
}

// PeopleTotals counts the accounts.
type PeopleTotals struct {
	Total   int `json:"total"`
	Admins  int `json:"admins"`
	Pending int `json:"pending"`
}

// ClusterStatus reads the registry and reports the cluster. It takes a store
// rather than a Server so the CLI can call it against the database directly,
// with no daemon running.
func ClusterStatus(s *store.Store, dataDir string, now time.Time) (*Status, error) {
	apps, err := s.Apps()
	if err != nil {
		return nil, err
	}
	nodes, err := s.Nodes()
	if err != nil {
		return nil, err
	}
	proxies, err := s.Proxies()
	if err != nil {
		return nil, err
	}
	users, err := s.Users()
	if err != nil {
		return nil, err
	}

	// Apps per node, so a node's row says how much it is carrying.
	perNode := make(map[string]int, len(nodes))
	totals := &AppTotals{Total: len(apps)}
	registered := make(map[string]bool, len(nodes))
	for _, n := range nodes {
		registered[n.Name] = true
	}
	for _, a := range apps {
		host := a.Host
		if host == "" {
			host = store.HostLocal
		}
		perNode[host]++
		if !registered[host] {
			totals.Unplaced++
		}
		if a.PoweredOff {
			totals.PoweredOff++
		}
		totals.DiskUsedMB += a.DiskMB
		snaps, err := s.Snapshots(a.Name)
		if err != nil {
			return nil, err
		}
		totals.Snapshots += len(snaps)
	}

	status := &Status{
		Apps:     totals,
		People:   &PeopleTotals{Total: len(users)},
		Snapshot: now,
		// Measured here and now: control does not heartbeat itself, and both
		// callers (the API inside control, the CLI on control's host) are
		// looking at the same machine.
		Control: &MemberStatus{Name: "control", Version: node.Version, LastSeen: now, Stats: stats.Measure(dataDir)},
	}
	for _, n := range nodes {
		status.Nodes = append(status.Nodes, &MemberStatus{
			Name:     n.Name,
			Address:  n.Address,
			Apps:     perNode[n.Name],
			LastSeen: n.LastSeen,
			Stale:    stale(n.LastSeen, now),
			Stats:    decodeStats(n.Stats),
		})
	}
	for _, p := range proxies {
		status.Proxies = append(status.Proxies, &MemberStatus{
			Name:     p.Name,
			Version:  p.Version,
			Routes:   p.Routes,
			LastSeen: p.LastSeen,
			Stale:    stale(p.LastSeen, now),
			Stats:    decodeStats(p.Stats),
		})
	}
	for _, u := range users {
		if u.Role == store.RoleAdmin {
			status.People.Admins++
		}
		if u.Status == store.StatusPending {
			status.People.Pending++
		}
	}
	sort.Slice(status.Nodes, func(i, j int) bool { return status.Nodes[i].Name < status.Nodes[j].Name })
	sort.Slice(status.Proxies, func(i, j int) bool { return status.Proxies[i].Name < status.Proxies[j].Name })
	return status, nil
}

// stale reports whether a member's last heartbeat is old enough to matter. A
// member that has never reported is stale by definition.
func stale(lastSeen, now time.Time) bool {
	return lastSeen.IsZero() || now.Sub(lastSeen) > staleAfter
}

// decodeStats unmarshals a member's reported machine stats. A member that
// never reported (or reported something this build cannot read) comes back
// zeroed, which renders as "--" rather than as a machine with no memory.
func decodeStats(blob string) stats.Stats {
	var s stats.Stats
	if blob == "" {
		return s
	}
	if err := json.Unmarshal([]byte(blob), &s); err != nil {
		return stats.Stats{}
	}
	return s
}
