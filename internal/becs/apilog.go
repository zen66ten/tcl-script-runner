package becs

import (
	"sync"
	"time"
)

// LogEntry is one recorded JSON-RPC message: a request, a response, or an error.
type LogEntry struct {
	Time     time.Time
	Dir      string // "→" request, "←" response, "✗" error
	Method   string
	ID       int
	Endpoint string
	Body     string // JSON body or error text (passwords redacted)
}

// Recorder is a thread-safe ring buffer of recent JSON-RPC traffic. It lets the
// web UI show what the runner is doing in the background without coupling the
// becs package to the web package.
type Recorder struct {
	mu      sync.Mutex
	entries []LogEntry
	max     int
}

// NewRecorder returns a Recorder that keeps at most max entries.
func NewRecorder(max int) *Recorder {
	return &Recorder{max: max}
}

// Record appends an entry, dropping the oldest once the buffer is full.
func (r *Recorder) Record(e LogEntry) {
	if r == nil {
		return
	}
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, e)
	if len(r.entries) > r.max {
		r.entries = r.entries[len(r.entries)-r.max:]
	}
}

// Entries returns a copy of the recorded entries, oldest first.
func (r *Recorder) Entries() []LogEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]LogEntry, len(r.entries))
	copy(out, r.entries)
	return out
}

// Clear discards all recorded entries.
func (r *Recorder) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = nil
}

// Log is the package-level recorder all clients write to.
var Log = NewRecorder(300)
