package client

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// Status is the lifecycle state of a Test.
type Status string

const (
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
)

// Test is one speed test run as exposed by the API.
type Test struct {
	ID         string            `json:"id"`
	Status     Status            `json:"status"`
	Phase      Phase             `json:"phase,omitempty"`
	Target     string            `json:"target"`
	Config     Config            `json:"config"`
	StartedAt  time.Time         `json:"started_at"`
	FinishedAt *time.Time        `json:"finished_at,omitempty"`
	Error      string            `json:"error,omitempty"`
	Progress   *Progress         `json:"progress,omitempty"`
	Latency    *LatencyResult    `json:"latency,omitempty"`
	Download   *ThroughputResult `json:"download,omitempty"`
	Upload     *ThroughputResult `json:"upload,omitempty"`
}

// ErrBusy is returned by Start while another test is running.
var ErrBusy = errors.New("a test is already running")

// Store keeps the most recent tests in memory. Only one test may run at a
// time: parallel tests would compete for the same link and skew each other.
type Store struct {
	mu      sync.Mutex
	limit   int
	tests   map[string]*Test
	order   []string // oldest first
	running string
}

// NewStore returns a Store that remembers up to limit tests.
func NewStore(limit int) *Store {
	return &Store{limit: limit, tests: make(map[string]*Test)}
}

// Start registers a new running test, or returns ErrBusy.
func (s *Store) Start(target string, cfg Config) (Test, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running != "" {
		return Test{}, ErrBusy
	}
	t := &Test{
		ID:        newID(),
		Status:    StatusRunning,
		Target:    target,
		Config:    cfg,
		StartedAt: time.Now().UTC(),
	}
	s.tests[t.ID] = t
	s.order = append(s.order, t.ID)
	if len(s.order) > s.limit {
		delete(s.tests, s.order[0])
		s.order = s.order[1:]
	}
	s.running = t.ID
	return *t, nil
}

// Update applies fn to the test with the given id under the store lock.
func (s *Store) Update(id string, fn func(*Test)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.tests[id]; ok {
		fn(t)
	}
}

// Finish marks a test done or failed and frees the runner slot.
func (s *Store) Finish(id string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running == id {
		s.running = ""
	}
	t, ok := s.tests[id]
	if !ok {
		return
	}
	now := time.Now().UTC()
	t.FinishedAt = &now
	t.Phase = ""
	t.Progress = nil
	t.Status = StatusDone
	if err != nil {
		t.Status = StatusFailed
		t.Error = err.Error()
	}
}

// Get returns a snapshot of one test.
func (s *Store) Get(id string) (Test, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tests[id]
	if !ok {
		return Test{}, false
	}
	return *t, true
}

// List returns snapshots of all remembered tests, newest first.
func (s *Store) List() []Test {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Test, 0, len(s.order))
	for i := len(s.order) - 1; i >= 0; i-- {
		out = append(out, *s.tests[s.order[i]])
	}
	return out
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
