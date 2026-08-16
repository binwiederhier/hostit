package node

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/app"
	"heckel.io/hostit/store"
)

// recordingCallbackStore captures what the callbacks write to the registry.
// hosts maps app name -> hosting node id, so the scoping guard can be exercised.
type recordingCallbackStore struct {
	power map[string]bool
	usage map[string]int
	snaps map[string]int
	hosts map[string]string
}

func (r *recordingCallbackStore) SetAppPoweredOff(name string, off bool) error {
	r.power[name] = off
	return nil
}

func (r *recordingCallbackStore) UpdateAppUsage(name string, usedMB int) error {
	r.usage[name] = usedMB
	return nil
}

func (r *recordingCallbackStore) ReplaceAppSnapshots(name string, snaps []*store.Snapshot) error {
	r.snaps[name] = len(snaps)
	return nil
}

func (r *recordingCallbackStore) AppHost(name string) (string, error) {
	if h, ok := r.hosts[name]; ok {
		return h, nil
	}
	return "", store.ErrAppNotFound
}

// TestCallbacksFlowOverTheDuplex is the full reverse path: the node's control
// sink posts over the same session the RPC rides on, and control's callback
// handler applies it to the registry.
func TestCallbacksFlowOverTheDuplex(t *testing.T) {
	t.Parallel()
	st := &recordingCallbackStore{power: map[string]bool{}, usage: map[string]int{}, snaps: map[string]int{}, hosts: map[string]string{"blog": "node-b"}}
	registered := make(chan struct{}, 1)
	srv := httptest.NewServer(ConnectHandler(
		func(string) bool { return true },
		func(nodeID string) http.Handler { return CallbackHandler(nodeID, st) },
		func(string, app.NodeAgent) { registered <- struct{}{} },
		nil,
	))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	conn, err := net.Dial("tcp", u.Host)
	require.NoError(t, err)
	link := NewControlLink()
	go func() {
		_ = ServeAgent(conn, "node-b", &fakeAgentFull{written: map[string][]byte{}}, link.SetClient)
	}()
	select {
	case <-registered:
	case <-time.After(3 * time.Second):
		t.Fatal("node never registered")
	}
	// Control registered first; wait for the node side to finish wiring its
	// reverse client (a callback posted before that is dropped by design).
	require.Eventually(t, func() bool {
		link.mu.Lock()
		defer link.mu.Unlock()
		return link.client != nil
	}, 3*time.Second, 5*time.Millisecond)

	link.PowerChanged("blog", true)
	link.UsageChanged("blog", 42)
	link.SnapshotsChanged("blog", []*store.Snapshot{{ID: "s1", AppName: "blog", CreatedAt: time.Now()}})

	assert.True(t, st.power["blog"])
	assert.Equal(t, 42, st.usage["blog"])
	assert.Equal(t, 1, st.snaps["blog"])
}

func TestCallbacksAreScopedToTheCallingNode(t *testing.T) {
	t.Parallel()
	// A node may only report control-plane data for apps IT hosts. "blog" is on
	// node-b (the caller); "victim" is on another node. The victim callbacks
	// must be rejected and leave the registry untouched -- a compromised node
	// must not flip another tenant's power, poison its usage, or wipe its
	// snapshot rows.
	st := &recordingCallbackStore{
		power: map[string]bool{}, usage: map[string]int{}, snaps: map[string]int{},
		hosts: map[string]string{"blog": "node-b", "victim": "node-c"},
	}
	h := CallbackHandler("node-b", st)

	post := func(kind, body string) int {
		req := httptest.NewRequest("POST", callbackPathPrefix+kind, strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	// Own app: accepted.
	assert.Equal(t, http.StatusOK, post("power", `{"name":"blog","powered_off":true}`))
	assert.True(t, st.power["blog"])

	// Another node's app: refused, no write.
	assert.Equal(t, http.StatusForbidden, post("power", `{"name":"victim","powered_off":true}`))
	assert.Equal(t, http.StatusForbidden, post("usage", `{"name":"victim","used_mb":9}`))
	assert.Equal(t, http.StatusForbidden, post("snapshots", `{"name":"victim","snapshots":[]}`))
	_, touchedPower := st.power["victim"]
	_, touchedUsage := st.usage["victim"]
	_, touchedSnaps := st.snaps["victim"]
	assert.False(t, touchedPower, "victim power untouched")
	assert.False(t, touchedUsage, "victim usage untouched")
	assert.False(t, touchedSnaps, "victim snapshots untouched")

	// An unknown app is refused too (no host to match).
	assert.Equal(t, http.StatusForbidden, post("power", `{"name":"ghost","powered_off":true}`))
}
