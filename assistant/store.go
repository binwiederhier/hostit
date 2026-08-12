package assistant

import (
	"sync"
)

// Usage is one turn's token counts, recorded per app so the platform can report
// what the built-in assistant has spent. Cache tokens are separate: they are
// priced differently from fresh input.
type Usage struct {
	InputTokens      int `json:"input_tokens"`
	OutputTokens     int `json:"output_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
}

// Store persists one conversation per app, so it survives a reload, a restart, or
// picking up from another device. The server backs this with SQLite; tests use
// the in-memory one.
type Store interface {
	Load(app string) ([]Message, error)
	Save(app string, messages []Message) error
	Delete(app string) error
	// RecordUsage adds one turn's token usage to the app's running totals.
	RecordUsage(app string, u Usage) error
}

// MemoryStore keeps transcripts in memory. It is the default and is all the tests
// need; nothing here is durable.
type MemoryStore struct {
	sessions map[string][]Message
	mu       sync.Mutex // Protects sessions
}

var _ Store = (*MemoryStore)(nil)

// NewMemoryStore returns an empty in-memory transcript store
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{sessions: make(map[string][]Message)}
}

func (s *MemoryStore) Load(app string) ([]Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Message(nil), s.sessions[app]...), nil
}

func (s *MemoryStore) Save(app string, messages []Message) error {
	s.mu.Lock()
	s.sessions[app] = append([]Message(nil), messages...)
	s.mu.Unlock()
	return nil
}

func (s *MemoryStore) Delete(app string) error {
	s.mu.Lock()
	delete(s.sessions, app)
	s.mu.Unlock()
	return nil
}

// RecordUsage is a no-op for the in-memory store; usage is only worth persisting.
func (s *MemoryStore) RecordUsage(app string, u Usage) error { return nil }
