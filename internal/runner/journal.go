package runner

import (
	"os"
	"sync"
)

// Journal is the append-only, newline-delimited record of a session's
// stream-json traffic. It is the immutable source of truth: even if the
// derived SQLite state is lost, a session can be fully reconstructed (and
// resumed) from its journal.
//
// Writes use the raw write(2) syscall (no userspace buffering), so bytes are
// visible to other file descriptors immediately — this is what lets a fresh
// reader replay the journal while the writer keeps appending. fsync is issued
// only at turn boundaries to bound durability cost while guaranteeing that a
// completed exchange survives power loss.
type Journal struct {
	mu sync.Mutex
	f  *os.File
}

// OpenJournal opens the journal at path for appending, creating it if absent.
// Reopening an existing journal (e.g. after a resume) continues appending.
func OpenJournal(path string) (*Journal, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &Journal{f: f}, nil
}

// Append writes one line followed by '\n'. When sync is true the data is
// flushed to stable storage before returning.
func (j *Journal) Append(line []byte, sync bool) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, err := j.f.Write(append(line, '\n')); err != nil {
		return err
	}
	if sync {
		return j.f.Sync()
	}
	return nil
}

// Close flushes and closes the journal file.
func (j *Journal) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.f.Sync()
	return j.f.Close()
}

// ReadJournal returns the raw bytes of the journal at path (all lines written so
// far), used to replay history to a newly connected client. A missing journal
// yields no bytes rather than an error.
func ReadJournal(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	return b, err
}
