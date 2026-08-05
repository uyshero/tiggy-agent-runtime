package reliability

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/agent"
	"github.com/uyshero/tiggy-agent-runtime/agenttest"
	"github.com/uyshero/tiggy-agent-runtime/event"
)

type scenario struct {
	id                   string
	category             string
	fault                string
	expected             Outcome
	requiredEvents       []event.Type
	expectedMaxDelegates map[string]int
	run                  func(context.Context) (observation, error)
}

type observation struct {
	result        agent.Result
	err           error
	delegateCalls map[string]int
}

func Run(ctx context.Context) Report {
	if ctx == nil {
		ctx = context.Background()
	}
	definitions := scenarios()
	results := make([]ScenarioResult, 0, len(definitions))
	for _, definition := range definitions {
		results = append(results, evaluateScenario(ctx, definition))
	}
	return Report{
		SchemaVersion: SchemaV1,
		GeneratedAt:   time.Now().UTC(),
		Summary:       summarize(results),
		Scenarios:     results,
	}
}

func evaluateScenario(ctx context.Context, definition scenario) ScenarioResult {
	started := time.Now()
	observed, harnessErr := definition.run(ctx)
	duration := time.Since(started).Microseconds()
	result := ScenarioResult{
		ID: definition.id, Category: definition.category, Fault: definition.fault,
		Expected: definition.expected, DelegateCalls: cloneCounts(observed.delegateCalls),
		ExpectedMaxDelegates: cloneCounts(definition.expectedMaxDelegates), DurationMicros: duration,
	}
	var runError *agent.RunError
	if errors.As(observed.err, &runError) && runError != nil {
		result.Observed = Outcome{Status: string(observed.result.Status), ErrorClass: string(runError.Class), ErrorCode: runError.Code}
	} else {
		result.Observed = Outcome{Status: string(observed.result.Status)}
	}
	traceErr := agenttest.ValidateTrace(observed.result.Events)
	result.Checks.TerminalInvariant = traceErr == nil
	stateErr := observed.result.State.ValidateComplete()
	if observed.result.Status == agent.StatusSuspended {
		stateErr = observed.result.State.ValidateSuspended()
	}
	result.Checks.StateInvariant = stateErr == nil
	result.Checks.Classification = harnessErr == nil && outcomeMatches(definition.expected, result.Observed) && errorPresenceMatches(definition.expected, observed.err)
	result.Checks.Diagnostics = diagnosticEvidence(observed.result.Events, definition.expected, definition.requiredEvents)
	result.Checks.NoImplicitReplay = noImplicitReplay(observed.delegateCalls, definition.expectedMaxDelegates)
	result.Passed = result.Checks.TerminalInvariant && result.Checks.StateInvariant && result.Checks.Classification && result.Checks.Diagnostics && result.Checks.NoImplicitReplay
	if !result.Passed {
		problems := make([]string, 0, 6)
		if harnessErr != nil {
			problems = append(problems, "harness: "+harnessErr.Error())
		}
		if traceErr != nil {
			problems = append(problems, "trace: "+traceErr.Error())
		}
		if stateErr != nil {
			problems = append(problems, "state: "+stateErr.Error())
		}
		if !result.Checks.Classification {
			problems = append(problems, fmt.Sprintf("outcome: expected %+v, observed %+v", definition.expected, result.Observed))
		}
		if !result.Checks.Diagnostics {
			problems = append(problems, "diagnostic evidence missing or inconsistent")
		}
		if !result.Checks.NoImplicitReplay {
			problems = append(problems, "delegate call was undeclared or exceeded the scenario maximum")
		}
		result.Failure = strings.Join(problems, "; ")
	}
	return result
}

func outcomeMatches(expected, observed Outcome) bool {
	return expected == observed
}

func errorPresenceMatches(expected Outcome, err error) bool {
	if expected.ErrorClass == "" {
		return err == nil
	}
	var runError *agent.RunError
	return errors.As(err, &runError) && runError != nil
}

func diagnosticEvidence(events []event.Event, expected Outcome, required []event.Type) bool {
	counts := make(map[event.Type]int)
	for _, value := range events {
		counts[value.Type]++
	}
	for _, eventType := range required {
		if counts[eventType] == 0 {
			return false
		}
	}
	if expected.ErrorClass == "" {
		return counts[event.RuntimeError] == 0
	}
	for _, value := range events {
		if value.Type != event.RuntimeError {
			continue
		}
		payload, err := agenttest.DecodePayload[event.ErrorPayload](value)
		if err == nil && payload.Class == expected.ErrorClass && payload.Code == expected.ErrorCode {
			return true
		}
	}
	return false
}

func noImplicitReplay(actual, maximum map[string]int) bool {
	for _, key := range countKeys(actual) {
		limit, constrained := maximum[key]
		if !constrained || actual[key] > limit {
			return false
		}
	}
	for _, key := range countKeys(maximum) {
		if actual[key] > maximum[key] {
			return false
		}
	}
	return true
}
