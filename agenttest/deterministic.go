package agenttest

import (
	"fmt"
	"sync"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/agent"
)

func SequentialIDs() agent.IDGenerator {
	var mu sync.Mutex
	counters := make(map[string]int)
	return func(prefix string) string {
		mu.Lock()
		defer mu.Unlock()
		counters[prefix]++
		return fmt.Sprintf("%s_%d", prefix, counters[prefix])
	}
}

func StepClock(start time.Time, step time.Duration) agent.Clock {
	var mu sync.Mutex
	next := start
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		value := next
		next = next.Add(step)
		return value
	}
}
