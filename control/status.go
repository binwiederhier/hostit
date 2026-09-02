package control

import (
	"encoding/json"
	"net"
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

// MemberStatus is one node or proxy. Address is this host's own IP for the
// colocated local node/proxy and empty for a remote proxy (control never dials
// one); Routes is empty for a node (only a proxy routes). Both a node and a
// proxy report their build version.
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

	// The address of THIS box, shared by every colocated member (control itself,
	// and the local node/proxy that never register one of their own).
	local := localIP()
	status := &Status{
		Apps:     totals,
		People:   &PeopleTotals{Total: len(users)},
		Snapshot: now,
		// Measured here and now: control does not heartbeat itself, and both
		// callers (the API inside control, the CLI on control's host) are
		// looking at the same machine.
		Control: &MemberStatus{Name: "control", Address: local, Version: node.Version, LastSeen: now, Stats: stats.Measure(dataDir)},
	}
	for _, n := range nodes {
		status.Nodes = append(status.Nodes, &MemberStatus{
			Name:     n.Name,
			Address:  nodeStatusAddress(n, local),
			Version:  n.Version,
			Apps:     perNode[n.Name],
			LastSeen: n.LastSeen,
			Stale:    stale(n.LastSeen, now),
			Stats:    decodeStats(n.Stats),
		})
	}
	for _, p := range proxies {
		status.Proxies = append(status.Proxies, &MemberStatus{
			Name:     p.Name,
			Address:  proxyStatusAddress(p.Name, local),
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

// localIP is the address other machines would reach this host at: the source IP
// the kernel picks for the default route. Colocated members (control itself, and
// the local node/proxy) never register an address of their own, so the status
// shows this rather than a bare loopback. The UDP "dial" opens no connection --
// it only runs the route lookup -- so nothing leaves the box; loopback is the
// fallback when there is no default route (an isolated host).
func localIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && addr.IP != nil {
		return addr.IP.String()
	}
	return "127.0.0.1"
}

// nodeStatusAddress is the address a caller would dial the node at. The local
// node shares control's host and never registers one of its own, so it takes
// control's address -- the same convention nodeAddress uses for routing.
func nodeStatusAddress(n *store.Node, local string) string {
	if n.Address == "" && n.Name == store.HostLocal {
		return local
	}
	return n.Address
}

// proxyStatusAddress is the local proxy's address (it shares control's host);
// control never dials a remote proxy, so those have none to show.
func proxyStatusAddress(name, local string) string {
	if name == store.ProxyLocal {
		return local
	}
	return ""
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
