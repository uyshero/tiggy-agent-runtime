package record

import (
	"encoding/json"
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/uyshero/tiggy-agent-runtime/event"
)

const (
	DefaultMaxActiveRuns   = 1024
	DefaultMaxEventsPerRun = 100_000
	DefaultMaxEventBytes   = 1 << 20
)

// Journal 持久化或保留不可变 Entry。
// Append 同步执行；若在 Recorder 外共享使用，必须并发安全。
type Journal interface {
	Append(Entry) error
}

// JournalFunc 将函数适配为 Journal。
type JournalFunc func(Entry) error

func (f JournalFunc) Append(entry Entry) error {
	return f(entry.Clone())
}

// Options 配置 Recorder 的脱敏与活跃 Run 边界。
type Options struct {
	Sanitizer       Sanitizer
	MaxActiveRuns   int
	MaxEventsPerRun int
	MaxEventBytes   int
}

type activeRun struct {
	lastDigest   string
	lastEvent    event.Event
	eventIDs     map[string]struct{}
	lastSequence int64
}

// Recorder 校验、脱敏、完整性链接并追加 Event Entry。
// 可被并发 Run 共享；对 Journal 的调用串行化。
type Recorder struct {
	mu              sync.Mutex
	journal         Journal
	sanitizer       Sanitizer
	maxActiveRuns   int
	maxEventsPerRun int
	maxEventBytes   int
	active          map[string]*activeRun
}

// New 构造 Recorder；Journal 必填，零值边界使用默认值。
func New(journal Journal, options Options) (*Recorder, error) {
	if journal == nil {
		return nil, fmt.Errorf("recording Journal is required")
	}
	maxActiveRuns := options.MaxActiveRuns
	if maxActiveRuns == 0 {
		maxActiveRuns = DefaultMaxActiveRuns
	}
	maxEventBytes := options.MaxEventBytes
	if maxEventBytes == 0 {
		maxEventBytes = DefaultMaxEventBytes
	}
	maxEventsPerRun := options.MaxEventsPerRun
	if maxEventsPerRun == 0 {
		maxEventsPerRun = DefaultMaxEventsPerRun
	}
	if maxActiveRuns < 1 || maxEventsPerRun < 1 || maxEventBytes < 1 {
		return nil, fmt.Errorf("recording bounds must be positive")
	}
	sanitizer := options.Sanitizer
	if sanitizer == nil {
		sanitizer = SafeSanitizer{}
	}
	return &Recorder{
		journal: journal, sanitizer: sanitizer,
		maxActiveRuns: maxActiveRuns, maxEventsPerRun: maxEventsPerRun, maxEventBytes: maxEventBytes,
		active: make(map[string]*activeRun),
	}, nil
}

// Sink 实现 event.Sink 回调形态。
// Journal/校验错误保留严格 Sink 语义；仅当丢失记录数据不得导致 Agent 失败时，
// 才用 observe.BestEffort 包装。
func (r *Recorder) Sink(value event.Event) error {
	if r == nil {
		return fmt.Errorf("Recorder is required")
	}
	if err := value.Validate(); err != nil {
		return fmt.Errorf("record Event: %w", err)
	}
	if value.ProtocolVersion != event.ProtocolV1 {
		return fmt.Errorf("unsupported Event protocol %q", value.ProtocolVersion)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	state, exists := r.active[value.RunID]
	if !exists {
		if value.Type != event.RunStarted || value.Sequence != 1 || value.ParentID != "" {
			return fmt.Errorf("recording must begin with an unparented run.started Event at sequence 1")
		}
		if len(r.active) >= r.maxActiveRuns {
			return fmt.Errorf("recording active Run limit reached")
		}
		state = &activeRun{eventIDs: make(map[string]struct{})}
	} else if err := validateNextEvent(state, value); err != nil {
		return err
	}
	if value.Sequence > int64(r.maxEventsPerRun) {
		return fmt.Errorf("recording per-Run Event limit reached")
	}
	if _, duplicate := state.eventIDs[value.ID]; duplicate {
		return fmt.Errorf("recording duplicated Event id %q", value.ID)
	}
	if value.ParentID != "" {
		if _, exists := state.eventIDs[value.ParentID]; !exists {
			return fmt.Errorf("recording Event references unknown or future parent %q", value.ParentID)
		}
	}

	sanitized, err := callSanitizer(r.sanitizer, cloneEvent(value))
	if err != nil {
		return err
	}
	if err := sanitized.Validate(value); err != nil {
		return err
	}
	encoded, err := json.Marshal(sanitized.Event)
	if err != nil {
		return fmt.Errorf("encode sanitized Event: %w", err)
	}
	if len(encoded) > r.maxEventBytes {
		return fmt.Errorf("sanitized Event exceeds %d bytes", r.maxEventBytes)
	}
	entry, err := newEntry(sanitized.Event, state.lastDigest, sanitized.PayloadRedacted)
	if err != nil {
		return err
	}
	if err := callJournal(r.journal, entry.Clone()); err != nil {
		return err
	}

	state.lastDigest = entry.Digest
	state.lastEvent = cloneEvent(value)
	state.lastSequence = value.Sequence
	state.eventIDs[value.ID] = struct{}{}
	if value.Terminal() {
		delete(r.active, value.RunID)
	} else if !exists {
		r.active[value.RunID] = state
	}
	return nil
}

func validateNextEvent(state *activeRun, value event.Event) error {
	if state.lastEvent.Terminal() {
		return fmt.Errorf("recording cannot continue after a terminal Event")
	}
	if value.Type == event.RunStarted {
		return fmt.Errorf("recording Run cannot start twice")
	}
	if value.Sequence != state.lastSequence+1 {
		return fmt.Errorf("recording Event sequence is not contiguous")
	}
	return nil
}

func callSanitizer(sanitizer Sanitizer, value event.Event) (result SanitizedEvent, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("recording Sanitizer panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	result, err = sanitizer.Sanitize(cloneEvent(value))
	if err != nil {
		return SanitizedEvent{}, fmt.Errorf("sanitize recorded Event: %w", err)
	}
	result.Event = cloneEvent(result.Event)
	return result, nil
}

func callJournal(journal Journal, entry Entry) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("recording Journal panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	if err := journal.Append(entry.Clone()); err != nil {
		return fmt.Errorf("append recording Entry: %w", err)
	}
	return nil
}
