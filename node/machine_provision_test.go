package node

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"heckel.io/hostit/nodeapi"
)

// A delete-then-recreate of the same name must not race the teardown. The
// teardown runs HERE, in the background, so the wait belongs here too: control
// used to check its own copy of that state, which is the same object only when
// the node is colocated. Against a remote node control's copy is always empty,
// so the create raced straight into "group already exists" (seen the first
// time the e2e suite ran across two machines).
func TestProvisionWaitsForAnInFlightTeardownOfTheSameName(t *testing.T) {
	t.Parallel()
	m := newSyncTestMachine(t)
	m.SetTearingDown("blog", true)
	done := make(chan struct{})
	go func() {
		time.Sleep(150 * time.Millisecond)
		m.SetTearingDown("blog", false)
		close(done)
	}()

	start := time.Now()
	m.awaitTeardown("blog")

	<-done
	assert.GreaterOrEqual(t, time.Since(start), 100*time.Millisecond, "provision waits for the teardown to finish")
	require.False(t, m.IsTearingDown("blog"))
}

// Nothing tearing down means no wait at all.
func TestProvisionDoesNotWaitWhenNothingIsTearingDown(t *testing.T) {
	t.Parallel()
	m := newSyncTestMachine(t)
	start := time.Now()
	m.awaitTeardown("blog")
	assert.Less(t, time.Since(start), 50*time.Millisecond)
	_ = nodeapi.ProvisionSpec{}
}
