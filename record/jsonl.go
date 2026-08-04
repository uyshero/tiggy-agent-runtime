package record

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
)

const (
	DefaultJSONLMaxLineBytes = 2 << 20
	DefaultReadMaxEntries    = 1_000_000
	DefaultReadMaxRecordings = 10_000
)

type JSONLOptions struct {
	MaxLineBytes    int
	MaxActiveRuns   int
	MaxEventsPerRun int
}

// JSONLJournal appends one Entry per line. The caller owns the Writer, flush,
// sync, close, file permissions, encryption, rotation, and retention policy.
// A write error may leave a partial final line.
type JSONLJournal struct {
	mu              sync.Mutex
	writer          io.Writer
	maxLineBytes    int
	maxActiveRuns   int
	maxEventsPerRun int
	active          map[string]*jsonlRun
}

type jsonlRun struct {
	last     Entry
	eventIDs map[string]struct{}
}

func NewJSONLJournal(writer io.Writer, options JSONLOptions) (*JSONLJournal, error) {
	if writer == nil {
		return nil, fmt.Errorf("JSONL Writer is required")
	}
	maxLineBytes := options.MaxLineBytes
	if maxLineBytes == 0 {
		maxLineBytes = DefaultJSONLMaxLineBytes
	}
	if maxLineBytes < 1 {
		return nil, fmt.Errorf("JSONL line bound must be positive")
	}
	maxActiveRuns := options.MaxActiveRuns
	if maxActiveRuns == 0 {
		maxActiveRuns = DefaultMaxActiveRuns
	}
	maxEventsPerRun := options.MaxEventsPerRun
	if maxEventsPerRun == 0 {
		maxEventsPerRun = DefaultMaxEventsPerRun
	}
	if maxActiveRuns < 1 || maxEventsPerRun < 1 {
		return nil, fmt.Errorf("JSONL active Run and Event bounds must be positive")
	}
	return &JSONLJournal{
		writer: writer, maxLineBytes: maxLineBytes,
		maxActiveRuns: maxActiveRuns, maxEventsPerRun: maxEventsPerRun,
		active: make(map[string]*jsonlRun),
	}, nil
}

func (j *JSONLJournal) Append(entry Entry) error {
	if j == nil {
		return fmt.Errorf("JSONL Journal is required")
	}
	entry = entry.Clone()
	j.mu.Lock()
	defer j.mu.Unlock()
	var previous *Entry
	state, exists := j.active[entry.RunID]
	if exists {
		previous = &state.last
	} else {
		if len(j.active) >= j.maxActiveRuns {
			return fmt.Errorf("JSONL active Run limit reached")
		}
		state = &jsonlRun{eventIDs: make(map[string]struct{})}
	}
	if err := entry.Validate(previous); err != nil {
		return err
	}
	if entry.Event.Sequence > int64(j.maxEventsPerRun) {
		return fmt.Errorf("JSONL per-Run Event limit reached")
	}
	if _, duplicate := state.eventIDs[entry.Event.ID]; duplicate {
		return fmt.Errorf("recording duplicated Event id %q", entry.Event.ID)
	}
	if entry.Event.ParentID != "" {
		if _, exists := state.eventIDs[entry.Event.ParentID]; !exists {
			return fmt.Errorf("recording Event references unknown or future parent %q", entry.Event.ParentID)
		}
	}
	encoded, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode JSONL Entry: %w", err)
	}
	if len(encoded)+1 > j.maxLineBytes {
		return fmt.Errorf("JSONL Entry exceeds %d bytes", j.maxLineBytes)
	}
	line := append(encoded, '\n')
	written, err := j.writer.Write(line)
	if err != nil {
		return fmt.Errorf("write JSONL Entry: %w", err)
	}
	if written != len(line) {
		return io.ErrShortWrite
	}
	if entry.Event.Terminal() {
		delete(j.active, entry.RunID)
	} else {
		state.last = entry
		state.eventIDs[entry.Event.ID] = struct{}{}
		j.active[entry.RunID] = state
	}
	return nil
}

type ReadOptions struct {
	MaxLineBytes    int
	MaxEntries      int
	MaxRecordings   int
	RequireComplete bool
}

// ReadJSONL reads interleaved Run Entries, preserving Run first-seen order.
// Incomplete recordings are returned by default for crash diagnostics.
func ReadJSONL(reader io.Reader, options ReadOptions) ([]Recording, error) {
	if reader == nil {
		return nil, fmt.Errorf("JSONL Reader is required")
	}
	maxLineBytes := options.MaxLineBytes
	if maxLineBytes == 0 {
		maxLineBytes = DefaultJSONLMaxLineBytes
	}
	maxEntries := options.MaxEntries
	if maxEntries == 0 {
		maxEntries = DefaultReadMaxEntries
	}
	maxRecordings := options.MaxRecordings
	if maxRecordings == 0 {
		maxRecordings = DefaultReadMaxRecordings
	}
	if maxLineBytes < 1 || maxEntries < 1 || maxRecordings < 1 {
		return nil, fmt.Errorf("JSONL read bounds must be positive")
	}

	scanner := bufio.NewScanner(reader)
	initial := 64 << 10
	if maxLineBytes < initial {
		initial = maxLineBytes
	}
	scanner.Buffer(make([]byte, initial), maxLineBytes)
	byRun := make(map[string]int)
	recordings := make([]Recording, 0)
	entryCount := 0
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			return nil, fmt.Errorf("JSONL line %d is empty", lineNumber)
		}
		entryCount++
		if entryCount > maxEntries {
			return nil, fmt.Errorf("JSONL Entry limit reached")
		}
		var entry Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, fmt.Errorf("decode JSONL line %d: %w", lineNumber, err)
		}
		index, exists := byRun[entry.RunID]
		if !exists {
			if len(recordings) >= maxRecordings {
				return nil, fmt.Errorf("JSONL Recording limit reached")
			}
			index = len(recordings)
			byRun[entry.RunID] = index
			recordings = append(recordings, Recording{ProtocolVersion: ProtocolV1, RunID: entry.RunID})
		}
		recordings[index].Entries = append(recordings[index].Entries, entry.Clone())
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read JSONL: %w", err)
	}
	for index, value := range recordings {
		var err error
		if options.RequireComplete {
			err = value.ValidateComplete()
		} else {
			err = value.Validate()
		}
		if err != nil {
			return nil, fmt.Errorf("Recording %d: %w", index, err)
		}
	}
	return recordings, nil
}
