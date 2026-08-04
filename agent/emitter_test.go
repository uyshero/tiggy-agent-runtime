package agent

import (
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/event"
)

func TestEmitterTreatsRunSuspendedAsTerminal(t *testing.T) {
	t.Parallel()

	sequence := 0
	emit := &emitter{
		runID: "run_1",
		ids: func(prefix string) string {
			sequence++
			return prefix + "_1"
		},
		now: func() time.Time { return time.Unix(1_700_000_000, 0) },
	}
	if _, err := emit.emit(event.RunSuspended, "", event.RunTerminalPayload{
		Status: "suspended", SuspensionID: "suspension_1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := emit.emit(event.RuntimeError, "", event.ErrorPayload{Code: "late"}); err == nil {
		t.Fatal("emitter accepted an event after run.suspended")
	}
}
