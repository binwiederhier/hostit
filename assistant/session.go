package assistant

import (
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
)

// ErrBusy is returned when a turn is already running for an app; only one runs at
// a time, so a second sender must wait rather than clobber the transcript.
var ErrBusy = errors.New("a turn is already in progress for this app")

// session is one app's live conversation state: whether a run is in progress and
// who is watching. The run itself lives in a server goroutine (not a request), so
// it keeps going when the sender leaves, and it publishes to every subscriber, so
// every browser and phone watching the app sees the same stream.
type session struct {
	running bool
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

func (s *session) subscribe() (int, chan Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextSub
	s.nextSub++
	ch := make(chan Event, subChanBuffer)
	s.subs[id] = ch
	return id, ch
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
	s.mu.Unlock()
}

func (s *session) isRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}
