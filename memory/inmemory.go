package memory

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"
)

// ConflictPolicy controls an admitted Item whose ID already exists in Scope.
type ConflictPolicy string

const (
	ConflictReject          ConflictPolicy = "reject"
	ConflictKeepExisting    ConflictPolicy = "keep_existing"
	ConflictReplaceExisting ConflictPolicy = "replace_existing"
)

const (
	// DefaultInMemoryMaxItems bounds all Items retained by one InMemory Port.
	DefaultInMemoryMaxItems = 1024
	// DefaultInMemoryMaxBytes bounds retained Item text to 4 MiB.
	DefaultInMemoryMaxBytes = 4 << 20
)

// Admission decides which, if any, Items a completed turn may add. Returning
// no Items makes Observe read-only. Implementations must be concurrency-safe.
type Admission interface {
	Admit(context.Context, ObserveRequest) ([]Item, error)
}

// AdmissionFunc adapts a function to Admission.
type AdmissionFunc func(context.Context, ObserveRequest) ([]Item, error)

func (f AdmissionFunc) Admit(ctx context.Context, request ObserveRequest) ([]Item, error) {
	return f(ctx, request.Clone())
}

// InMemoryOptions configures a bounded process-local reference Port.
type InMemoryOptions struct {
	InitialItems   []Item
	Admission      Admission
	MemoryOptions  Options
	MaxStoredItems int
	MaxStoredBytes int
	ConflictPolicy ConflictPolicy
	Now            func() time.Time
}

// InMemory is a concurrency-safe, non-durable reference Port. Recall returns
// the newest matching Items first. Observe stores only Items explicitly
// returned by Admission. It never silently evicts Items; exact-ID conflicts
// follow the configured ConflictPolicy.
type InMemory struct {
	mu sync.RWMutex

	admission Admission
	limits    Options
	maxItems  int
	maxBytes  int
	conflicts ConflictPolicy
	now       func() time.Time

	items     map[Scope][]Item
	itemIDs   map[Scope]map[string]struct{}
	itemCount int
	textBytes int
}

// NewInMemory validates and copies all configuration and initial Items.
func NewInMemory(options InMemoryOptions) (*InMemory, error) {
	limits, err := NormalizeOptions(options.MemoryOptions)
	if err != nil {
		return nil, err
	}
	maxItems := options.MaxStoredItems
	if maxItems == 0 {
		maxItems = DefaultInMemoryMaxItems
	}
	maxBytes := options.MaxStoredBytes
	if maxBytes == 0 {
		maxBytes = DefaultInMemoryMaxBytes
	}
	conflicts := options.ConflictPolicy
	if conflicts == "" {
		conflicts = ConflictReject
	}
	if maxItems < 0 || maxBytes < limits.MaxItemBytes {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: fmt.Errorf("in-memory bounds require positive item capacity and stored bytes covering one item")}
	}
	if conflicts != ConflictReject && conflicts != ConflictKeepExisting && conflicts != ConflictReplaceExisting {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: fmt.Errorf("unsupported in-memory conflict policy %q", conflicts)}
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	value := &InMemory{
		admission: options.Admission, limits: limits, maxItems: maxItems, maxBytes: maxBytes,
		conflicts: conflicts, now: now,
		items: make(map[Scope][]Item), itemIDs: make(map[Scope]map[string]struct{}),
	}
	initial := cloneItems(options.InitialItems)
	if err := value.validateItems(initial, nil, nil); err != nil {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: err}
	}
	if err := value.appendItems(initial); err != nil {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: err}
	}
	return value, nil
}

func (m *InMemory) Recall(ctx context.Context, request RecallRequest) (RecallResult, error) {
	if ctx == nil || m == nil {
		return RecallResult{}, &Error{Code: ErrorInvalidRequest, Cause: fmt.Errorf("context and in-memory Port are required")}
	}
	if err := ctx.Err(); err != nil {
		return RecallResult{}, err
	}
	request = request.Clone()
	if err := request.Validate(); err != nil {
		return RecallResult{}, &Error{Code: ErrorInvalidRequest, Cause: err}
	}
	limit := request.Limit
	if limit > m.limits.RecallLimit {
		limit = m.limits.RecallLimit
	}
	now, err := callMemoryClock(m.now)
	if err != nil {
		return RecallResult{}, err
	}

	m.mu.RLock()
	stored := m.items[request.Scope]
	items := make([]Item, 0, min(limit, len(stored)))
	total := 0
	for index := len(stored) - 1; index >= 0 && len(items) < limit; index-- {
		if expired(stored[index], now) {
			continue
		}
		next := len(stored[index].Text)
		if total+next > m.limits.MaxRecallBytes {
			break
		}
		items = append(items, stored[index])
		total += next
	}
	m.mu.RUnlock()
	if err := ctx.Err(); err != nil {
		return RecallResult{}, err
	}
	return RecallResult{Items: cloneItems(items)}, nil
}

func (m *InMemory) Observe(ctx context.Context, request ObserveRequest) error {
	if ctx == nil || m == nil {
		return &Error{Code: ErrorInvalidRequest, Cause: fmt.Errorf("context and in-memory Port are required")}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	request = request.Clone()
	if err := request.Validate(m.limits); err != nil {
		return &Error{Code: ErrorInvalidRequest, Cause: err}
	}
	if m.admission == nil {
		return ctx.Err()
	}
	items, err := callAdmission(ctx, m.admission, request)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if err != nil {
		var memoryError *Error
		if errors.As(err, &memoryError) && memoryError.Code == ErrorPanic {
			return err
		}
		return &Error{Code: ErrorAdmission, Cause: err}
	}
	items = cloneItems(items)
	now, clockErr := callMemoryClock(m.now)
	if clockErr != nil {
		return clockErr
	}
	if err := m.validateItems(items, &request, &now); err != nil {
		return &Error{Code: ErrorInvalidResult, Cause: err}
	}
	if len(items) == 0 {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	m.pruneExpired(now)
	return m.appendItems(items)
}

func (m *InMemory) validateItems(items []Item, observation *ObserveRequest, now *time.Time) error {
	if len(items) > m.maxItems {
		return fmt.Errorf("memory item batch exceeds in-memory item capacity")
	}
	batchIDs := make(map[Scope]map[string]struct{})
	batchBytes := 0
	for index, item := range items {
		if err := item.Validate(); err != nil {
			return fmt.Errorf("memory item %d: %w", index, err)
		}
		if observation != nil && item.Scope != observation.Scope {
			return fmt.Errorf("memory item %d crossed the observed scope", index)
		}
		if observation != nil {
			if err := validateObservedProvenance(item.Provenance, *observation); err != nil {
				return fmt.Errorf("memory item %d provenance: %w", index, err)
			}
		}
		if now != nil && expired(item, *now) {
			return fmt.Errorf("memory item %d is already expired", index)
		}
		if len(item.Text) > m.limits.MaxItemBytes {
			return fmt.Errorf("memory item %d exceeds the item limit", index)
		}
		ids := batchIDs[item.Scope]
		if ids == nil {
			ids = make(map[string]struct{})
			batchIDs[item.Scope] = ids
		}
		if _, exists := ids[item.ID]; exists {
			return fmt.Errorf("duplicate memory item id %q in scope", item.ID)
		}
		ids[item.ID] = struct{}{}
		batchBytes += len(item.Text)
		if batchBytes > m.maxBytes {
			return fmt.Errorf("memory item batch exceeds in-memory byte capacity")
		}
	}
	return nil
}

// appendItems validates storage conflicts and capacity before changing state.
// The caller holds m.mu after construction has completed.
func (m *InMemory) appendItems(items []Item) error {
	pending := items
	replacedIDs := make(map[Scope]map[string]struct{})
	replacedCount := 0
	replacedBytes := 0
	addedBytes := 0
	if m.conflicts == ConflictKeepExisting {
		pending = make([]Item, 0, len(items))
	}
	for _, item := range items {
		_, exists := m.itemIDs[item.Scope][item.ID]
		if exists {
			switch m.conflicts {
			case ConflictReject:
				return &Error{Code: ErrorConflict, Cause: fmt.Errorf("memory item %q already exists in scope", item.ID)}
			case ConflictKeepExisting:
				continue
			case ConflictReplaceExisting:
				if replacedIDs[item.Scope] == nil {
					replacedIDs[item.Scope] = make(map[string]struct{})
				}
				replacedIDs[item.Scope][item.ID] = struct{}{}
				replacedCount++
				replacedBytes += m.existingTextBytes(item.Scope, item.ID)
			}
		}
		if m.conflicts == ConflictKeepExisting {
			pending = append(pending, item)
		}
		addedBytes += len(item.Text)
	}
	if m.itemCount-replacedCount+len(pending) > m.maxItems {
		return &Error{Code: ErrorLimit, Cause: fmt.Errorf("in-memory item capacity reached")}
	}
	if m.textBytes-replacedBytes+addedBytes > m.maxBytes {
		return &Error{Code: ErrorLimit, Cause: fmt.Errorf("in-memory byte capacity reached")}
	}
	if m.conflicts == ConflictReplaceExisting {
		m.removeItems(replacedIDs)
	}
	for _, item := range pending {
		if m.itemIDs[item.Scope] == nil {
			m.itemIDs[item.Scope] = make(map[string]struct{})
		}
		m.itemIDs[item.Scope][item.ID] = struct{}{}
		m.items[item.Scope] = append(m.items[item.Scope], item)
	}
	m.itemCount += len(pending)
	m.textBytes += addedBytes
	return nil
}

func (m *InMemory) existingTextBytes(scope Scope, id string) int {
	for _, item := range m.items[scope] {
		if item.ID == id {
			return len(item.Text)
		}
	}
	return 0
}

func (m *InMemory) removeItems(ids map[Scope]map[string]struct{}) {
	for scope, remove := range ids {
		stored := m.items[scope]
		retained := stored[:0]
		for _, item := range stored {
			if _, exists := remove[item.ID]; exists {
				delete(m.itemIDs[scope], item.ID)
				m.itemCount--
				m.textBytes -= len(item.Text)
				continue
			}
			retained = append(retained, item)
		}
		m.items[scope] = retained
	}
}

func (m *InMemory) pruneExpired(now time.Time) {
	for scope, stored := range m.items {
		retained := stored[:0]
		for _, item := range stored {
			if expired(item, now) {
				delete(m.itemIDs[scope], item.ID)
				m.itemCount--
				m.textBytes -= len(item.Text)
				continue
			}
			retained = append(retained, item)
		}
		if len(retained) == 0 {
			delete(m.items, scope)
			delete(m.itemIDs, scope)
		} else {
			m.items[scope] = retained
		}
	}
}

func validateObservedProvenance(provenance Provenance, request ObserveRequest) error {
	if provenance.RunID != request.RunID {
		return fmt.Errorf("run id must match the observed run")
	}
	if len(provenance.MessageIDs) == 0 {
		return fmt.Errorf("at least one source message id is required")
	}
	allowed := make(map[string]struct{}, len(request.Messages))
	for _, value := range request.Messages {
		allowed[value.ID] = struct{}{}
	}
	for _, id := range provenance.MessageIDs {
		if _, exists := allowed[id]; !exists {
			return fmt.Errorf("message id %q is not part of the observed turn", id)
		}
	}
	return nil
}

func expired(item Item, now time.Time) bool {
	return item.Expired(now)
}

func callMemoryClock(clock func() time.Time) (value time.Time, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &Error{Code: ErrorPanic, Cause: fmt.Errorf("memory clock panic: %v\n%s", recovered, debug.Stack())}
		}
	}()
	value = clock().UTC()
	if value.IsZero() {
		return time.Time{}, &Error{Code: ErrorInvalidConfig, Cause: fmt.Errorf("memory clock returned zero time")}
	}
	return value, nil
}

func callAdmission(ctx context.Context, admission Admission, request ObserveRequest) (items []Item, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = &Error{Code: ErrorPanic, Cause: fmt.Errorf("memory admission panic: %v\n%s", recovered, debug.Stack())}
		}
	}()
	items, err = admission.Admit(ctx, request.Clone())
	return append([]Item(nil), items...), err
}
