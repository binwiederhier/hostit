package node

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/app"
	"heckel.io/hostit/store"
)

// recordingCallbackStore captures what the callbacks write to the registry.
type recordingCallbackStore struct {
	power map[string]bool
	usage map[string]int
	snaps map[string]int
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

// TestCallbacksFlowOverTheDuplex is the full reverse path: the node's control
// sink posts over the same session the RPC rides on, and control's callback
// handler applies it to the registry.
func TestCallbacksFlowOverTheDuplex(t *testing.T) {
	t.Parallel()
	st := &recordingCallbackStore{power: map[string]bool{}, usage: map[string]int{}, snaps: map[string]int{}}
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

	link.PowerChanged("blog", true)
	link.UsageChanged("blog", 42)
	link.SnapshotsChanged("blog", []*store.Snapshot{{ID: "s1", AppName: "blog", CreatedAt: time.Now()}})

	assert.True(t, st.power["blog"])
	assert.Equal(t, 42, st.usage["blog"])
	assert.Equal(t, 1, st.snaps["blog"])
}
