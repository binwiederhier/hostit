package assistant

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	// runTimeout bounds one background run so a wedged model call cannot hold an
	// app's session forever
	runTimeout = 15 * time.Minute
	// subChanBuffer is how many events a slow subscriber may fall behind before it
	// is dropped (it recovers by reloading the transcript)
	subChanBuffer = 256
	// maxSubsPerApp caps concurrent watchers of one app's stream, so a client
	// cannot exhaust the server by opening endless connections
	maxSubsPerApp = 64
)

// ErrBusy is returned when a turn is already running for an app; only one runs at
// a time, so a second sender must wait rather than clobber the transcript.
var (
	ErrBusy = errors.New("a turn is already in progress for this app")
)

// ErrTooManySubscribers is returned when an app already has the most stream
// watchers it allows
var (
	ErrTooManySubscribers = errors.New("too many watchers for this app's assistant")
)

// session is one app's live conversation state: whether a run is in progress and
// who is watching. The run itself lives in a server goroutine (not a request), so
// it keeps going when the sender leaves, and it publishes to every subscriber, so
// every browser and phone watching the app sees the same stream.
type session struct {
	running bool
	cancel  context.CancelFunc // Cancels the in-progress run, so Stop can end it
	subs    map[int]chan Event
	nextSub int
	mu      sync.Mutex // Protects all of the above
}

func newSession() *session {
	return &session{subs: make(map[int]chan Event)}
}

// publish fans one event out to every subscriber. A subscriber whose buffer is
// full is dropped rather than allowed to stall the run; it recovers on reload.
func (s *session) publish(ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, ch := range s.subs {
		select {
		case ch <- ev:
		default:
			delete(s.subs, id)
			close(ch)
		}
	}
}

// subscribe registers a watcher, or returns ok=false when the per-app cap is hit
func (s *session) subscribe() (int, chan Event, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.subs) >= maxSubsPerApp {
		return 0, nil, false
	}
	id := s.nextSub
	s.nextSub++
	ch := make(chan Event, subChanBuffer)
	s.subs[id] = ch
	return id, ch, true
}

func (s *session) unsubscribe(id int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ch, ok := s.subs[id]; ok {
		delete(s.subs, id)
		close(ch)
	}
}

// begin claims the session for a run; false means one is already going.
func (s *session) begin() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return false
	}
	s.running = true
	return true
}

func (s *session) end() {
	s.mu.Lock()
	s.running = false
	s.cancel = nil
	s.mu.Unlock()
}

func (s *session) isRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// setCancel records how to cancel the in-progress run
func (s *session) setCancel(cancel context.CancelFunc) {
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()
}

// stop cancels the in-progress run, if any; it reports whether there was one.
func (s *session) stop() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running && s.cancel != nil {
		s.cancel()
		return true
	}
	return false
}
