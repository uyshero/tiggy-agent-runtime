package record

import (
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

const (
	DefaultMemoryMaxEntries = 100_000
	DefaultMemoryMaxBytes   = 64 << 20
)

type MemoryOptions struct {
	MaxEntries int
	MaxBytes   int
}

// MemoryJournal is a bounded, process-local Journal for tests and diagnostics.
// It never evicts Entries and loses all data when the process exits.
type MemoryJournal struct {
	mu         sync.RWMutex
	maxEntries int
	maxBytes   int
	entryCount int
	byteCount  int
	runs       map[string][]Entry
	eventIDs   map[string]map[string]struct{}
}

func NewMemoryJournal(options MemoryOptions) (*MemoryJournal, error) {
	maxEntries := options.MaxEntries
	if maxEntries == 0 {
		maxEntries = DefaultMemoryMaxEntries
	}
	maxBytes := options.MaxBytes
	if maxBytes == 0 {
		maxBytes = DefaultMemoryMaxBytes
	}
	if maxEntries < 1 || maxBytes < 1 {
		return nil, fmt.Errorf("Memory Journal bounds must be positive")
	}
	return &MemoryJournal{
		maxEntries: maxEntries, maxBytes: maxBytes,
		runs: make(map[string][]Entry), eventIDs: make(map[string]map[string]struct{}),
	}, nil
}

func (m *MemoryJournal) Append(entry Entry) error {
	if m == nil {
		return fmt.Errorf("Memory Journal is required")
	}
	entry = entry.Clone()
	encoded, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode recording Entry: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	stored := m.runs[entry.RunID]
	var previous *Entry
	if len(stored) > 0 {
		previous = &stored[len(stored)-1]
	}
	if err := entry.Validate(previous); err != nil {
		return err
	}
	ids := m.eventIDs[entry.RunID]
	if ids == nil {
		ids = make(map[string]struct{})
	}
	if _, exists := ids[entry.Event.ID]; exists {
		return fmt.Errorf("recording duplicated Event id %q", entry.Event.ID)
	}
	if entry.Event.ParentID != "" {
		if _, exists := ids[entry.Event.ParentID]; !exists {
			return fmt.Errorf("recording Event references unknown or future parent %q", entry.Event.ParentID)
		}
	}
	if m.entryCount+1 > m.maxEntries || m.byteCount+len(encoded) > m.maxBytes {
		return fmt.Errorf("Memory Journal capacity reached")
	}
	m.runs[entry.RunID] = append(stored, entry)
	ids[entry.Event.ID] = struct{}{}
	m.eventIDs[entry.RunID] = ids
	m.entryCount++
	m.byteCount += len(encoded)
	return nil
}

func (m *MemoryJournal) Recording(runID string) (Recording, bool) {
	if m == nil {
		return Recording{}, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	entries, exists := m.runs[runID]
	if !exists {
		return Recording{}, false
	}
	return newRecording(runID, entries), true
}

// Recordings returns independent snapshots ordered by Run ID.
func (m *MemoryJournal) Recordings() []Recording {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	runIDs := make([]string, 0, len(m.runs))
	for runID := range m.runs {
		runIDs = append(runIDs, runID)
	}
	sort.Strings(runIDs)
	result := make([]Recording, len(runIDs))
	for index, runID := range runIDs {
		result[index] = newRecording(runID, m.runs[runID])
	}
	return result
}

func newRecording(runID string, entries []Entry) Recording {
	value := Recording{ProtocolVersion: ProtocolV1, RunID: runID, Entries: make([]Entry, len(entries))}
	for index, entry := range entries {
		value.Entries[index] = entry.Clone()
	}
	return value
}
