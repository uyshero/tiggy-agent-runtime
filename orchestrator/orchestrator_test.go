package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/agent"
	"github.com/uyshero/tiggy-agent-runtime/agenttest"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

type scriptedSubagent struct {
	order *[]string
	fn    func(TaskSpec, []Artifact) (Artifact, error)
}

func (s scriptedSubagent) Execute(_ context.Context, task TaskSpec, inputs []Artifact) (Artifact, error) {
	*s.order = append(*s.order, task.ID)
	return s.fn(task, inputs)
}

type verifierFunc func(context.Context, TaskSpec, Artifact) error

func (f verifierFunc) Verify(ctx context.Context, task TaskSpec, output Artifact) error {
	return f(ctx, task, output)
}

type factoryFunc func(context.Context, string, TaskSpec) (Subagent, error)

func (f factoryFunc) Create(ctx context.Context, executionID string, task TaskSpec) (Subagent, error) {
	return f(ctx, executionID, task)
}

func validTask(id, subagent string, inputs ...string) TaskSpec {
	refs := make([]ArtifactInput, len(inputs))
	for i, value := range inputs {
		refs[i] = ArtifactInput{TaskID: value}
	}
	return TaskSpec{ID: id, Objective: "do " + id, Subagent: subagent, InputArtifacts: refs, Output: ArtifactSpec{Name: id + ".json", MediaType: "application/json"}}
}

func output(task TaskSpec, value string) Artifact {
	data := []byte(fmt.Sprintf("%q", value))
	return Artifact{Name: task.Output.Name, MediaType: task.Output.MediaType, Data: data, Digest: digest(data)}
}

func TestRunSequentiallyAndPassesOwnedInputs(t *testing.T) {
	order := []string{}
	brief := Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{validTask("one", "worker"), validTask("two", "worker", "one")}}
	var gotInput []byte
	o := Orchestrator{Subagents: Subagents{"worker": scriptedSubagent{order: &order, fn: func(task TaskSpec, inputs []Artifact) (Artifact, error) {
		if task.ID == "two" {
			if len(inputs) != 1 {
				return Artifact{}, fmt.Errorf("inputs = %d", len(inputs))
			}
			gotInput = append([]byte(nil), inputs[0].Data...)
			inputs[0].Data[0] = 'x'
		}
		return output(task, task.ID), nil
	}}}}
	report, err := o.Run(context.Background(), brief)
	if err != nil || report.Status != StatusCompleted {
		t.Fatalf("run = %#v, err = %v", report, err)
	}
	if !reflect.DeepEqual(order, []string{"one", "two"}) {
		t.Fatalf("order = %#v", order)
	}
	if string(gotInput) != `"one"` {
		t.Fatalf("input = %q", gotInput)
	}
	if len(report.Artifacts) != 2 || report.Tasks[1].Status != TaskCompleted {
		t.Fatalf("report = %#v", report)
	}
}

func TestRunPassesOwnedTaskSpec(t *testing.T) {
	order := []string{}
	brief := Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{validTask("one", "worker"), validTask("two", "worker", "one")}}
	o := Orchestrator{Subagents: Subagents{"worker": scriptedSubagent{order: &order, fn: func(task TaskSpec, _ []Artifact) (Artifact, error) {
		if len(task.InputArtifacts) > 0 {
			task.InputArtifacts[0].TaskID = "mutated"
		}
		return output(task, task.ID), nil
	}}}}
	if _, err := o.Run(context.Background(), brief); err != nil {
		t.Fatal(err)
	}
	if brief.Tasks[1].InputArtifacts[0].TaskID != "one" {
		t.Fatalf("brief was mutated: %#v", brief)
	}
}

func TestRunStopsAfterVerificationFailure(t *testing.T) {
	order := []string{}
	brief := Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{validTask("one", "worker"), validTask("two", "worker")}}
	o := Orchestrator{
		Subagents: Subagents{"worker": scriptedSubagent{order: &order, fn: func(task TaskSpec, _ []Artifact) (Artifact, error) { return output(task, task.ID), nil }}},
		Verifier: verifierFunc(func(_ context.Context, task TaskSpec, _ Artifact) error {
			if task.ID == "one" {
				return errors.New("not acceptable")
			}
			return nil
		}),
	}
	report, err := o.Run(context.Background(), brief)
	if err == nil || report.Status != StatusFailed {
		t.Fatalf("run = %#v, err = %v", report, err)
	}
	if !reflect.DeepEqual(order, []string{"one"}) || len(report.Tasks) != 2 || report.Tasks[0].Status != TaskVerificationFailed || report.Tasks[1].Status != TaskSkipped {
		t.Fatalf("order/report = %#v / %#v", order, report)
	}
}

func TestTaskVerifierTakesPrecedenceOverGlobalVerifier(t *testing.T) {
	task := validTask("one", "worker")
	task.Verification = &VerificationSpec{Verifier: "task"}
	brief := Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{task}}
	globalCalls, taskCalls := 0, 0
	o := Orchestrator{
		Subagents: Subagents{"worker": scriptedSubagent{order: new([]string), fn: func(task TaskSpec, _ []Artifact) (Artifact, error) { return output(task, task.ID), nil }}},
		Verifier: verifierFunc(func(context.Context, TaskSpec, Artifact) error {
			globalCalls++
			return errors.New("global should not run")
		}),
		Verifiers: Verifiers{"task": verifierFunc(func(context.Context, TaskSpec, Artifact) error { taskCalls++; return nil })},
	}
	report, err := o.Run(context.Background(), brief)
	if err != nil || report.Status != StatusCompleted || globalCalls != 0 || taskCalls != 1 {
		t.Fatalf("report = %#v, calls = %d/%d, err = %v", report, globalCalls, taskCalls, err)
	}
}

func TestBuiltinVerifiers(t *testing.T) {
	t.Run("json schema", func(t *testing.T) {
		task := validTask("one", "worker")
		task.Verification = &VerificationSpec{Verifier: VerifierJSONSchema, Config: json.RawMessage(`{"type":"string","const":"one"}`)}
		report, err := (Orchestrator{
			Subagents: Subagents{"worker": scriptedSubagent{order: new([]string), fn: func(task TaskSpec, _ []Artifact) (Artifact, error) { return output(task, task.ID), nil }}},
			Verifiers: BuiltinVerifiers(),
		}).Run(context.Background(), Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{task}})
		if err != nil || report.Status != StatusCompleted {
			t.Fatalf("report = %#v, err = %v", report, err)
		}
	})

	t.Run("text failure", func(t *testing.T) {
		task := validTask("one", "worker")
		task.Output = ArtifactSpec{Name: "one.txt", MediaType: "text/plain"}
		task.Verification = &VerificationSpec{Verifier: VerifierText, Config: json.RawMessage(`{"prefix":"required"}`)}
		report, err := (Orchestrator{
			Subagents: Subagents{"worker": scriptedSubagent{order: new([]string), fn: func(task TaskSpec, _ []Artifact) (Artifact, error) {
				return NewArtifact(task.Output.Name, task.Output.MediaType, []byte("wrong")), nil
			}}},
			Verifiers: BuiltinVerifiers(),
		}).Run(context.Background(), Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{task}})
		var runErr *RunError
		if !errors.As(err, &runErr) || runErr.Class != ErrorVerification || runErr.Code != "verification_failed" || report.Tasks[0].Status != TaskVerificationFailed {
			t.Fatalf("report = %#v, run error = %#v, err = %v", report, runErr, err)
		}
	})
}

func TestStructuredErrorDoesNotExposeCause(t *testing.T) {
	secret := errors.New("database password is secret")
	task := validTask("one", "worker")
	brief := Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{task}}
	var events []Event
	o := Orchestrator{Subagents: Subagents{
		"worker": scriptedSubagent{order: new([]string), fn: func(TaskSpec, []Artifact) (Artifact, error) {
			return Artifact{}, secret
		}},
	}}
	report, err := o.RunWithSink(context.Background(), brief, func(event Event) error {
		events = append(events, event)
		return nil
	})
	var runErr *RunError
	if !errors.As(err, &runErr) || !errors.Is(err, secret) || runErr.Class != ErrorExecution || runErr.Code != "subagent_failed" {
		t.Fatalf("run error = %#v, err = %v", runErr, err)
	}
	encoded, marshalErr := json.Marshal(struct {
		Report Report  `json:"report"`
		Events []Event `json:"events"`
	}{report, events})
	if marshalErr != nil || strings.Contains(string(encoded), "password") || strings.Contains(string(encoded), "secret") {
		t.Fatalf("serialized diagnostics leaked cause: %s, err = %v", encoded, marshalErr)
	}
}

func TestBriefRejectsForwardReference(t *testing.T) {
	brief := Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{validTask("one", "worker", "two"), validTask("two", "worker")}}
	if err := brief.Validate(); err == nil {
		t.Fatal("expected forward reference error")
	}
}

func TestRunCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	brief := Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{validTask("one", "worker")}}
	o := Orchestrator{Subagents: Subagents{"worker": scriptedSubagent{order: new([]string), fn: func(task TaskSpec, _ []Artifact) (Artifact, error) { return output(task, "x"), nil }}}}
	report, err := o.Run(ctx, brief)
	if !errors.Is(err, context.Canceled) || report.Status != StatusCanceled || report.Tasks[0].Status != TaskCanceled {
		t.Fatalf("report = %#v, err = %v", report, err)
	}
}

func TestAgentSubagent(t *testing.T) {
	modelValue := agenttest.NewScriptedModel(agenttest.ModelStep{Response: model.Response{
		Message:    message.Message{ID: "assistant-1", Role: message.RoleAssistant, Visibility: message.VisibilityPublic, Content: []message.Content{{Type: message.ContentText, Text: `{"status":"done"}`}}},
		StopReason: model.StopReasonComplete,
	}})
	value, err := agent.New(agent.Config{
		Name: "orchestrator-test", Model: modelValue,
		ModelIdentity: model.Identity{Provider: "test", Model: "scripted"},
		IDs:           agenttest.SequentialIDs(), Now: agenttest.StepClock(time.Unix(1_700_000_000, 0), time.Millisecond),
	})
	if err != nil {
		t.Fatal(err)
	}
	task := validTask("one", "agent")
	artifact, err := (AgentSubagent{Agent: value}).Execute(context.Background(), task, nil)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if err := artifact.Validate(task.Output); err != nil {
		t.Fatalf("artifact: %v", err)
	}
}

func TestFactoryCreatesOneExecutorPerTask(t *testing.T) {
	brief := Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{validTask("one", "worker"), validTask("two", "worker")}}
	created := []string{}
	o := Orchestrator{
		Factories: SubagentFactories{"worker": factoryFunc(func(_ context.Context, executionID string, task TaskSpec) (Subagent, error) {
			if executionID != "execution-1" {
				return nil, fmt.Errorf("execution id = %q", executionID)
			}
			created = append(created, task.ID)
			return scriptedSubagent{order: new([]string), fn: func(task TaskSpec, _ []Artifact) (Artifact, error) { return output(task, task.ID), nil }}, nil
		})},
		IDs: func(prefix string) string { return prefix + "-1" },
	}
	report, err := o.Run(context.Background(), brief)
	if err != nil || report.ExecutionID != "execution-1" || !reflect.DeepEqual(created, []string{"one", "two"}) {
		t.Fatalf("report = %#v, created = %#v, err = %v", report, created, err)
	}
}

func TestAgentSubagentProjectsArtifactContent(t *testing.T) {
	modelValue := agenttest.NewScriptedModel(agenttest.ModelStep{
		Assert: func(request model.Request) error {
			last := request.Messages[len(request.Messages)-1]
			text := last.Content[0].Text
			if !strings.Contains(text, `\"answer\":42`) || !strings.Contains(text, "sha256:") {
				return fmt.Errorf("projected input = %q", text)
			}
			return nil
		},
		Response: model.Response{Message: message.Message{Content: []message.Content{{Type: message.ContentText, Text: "# Report\n\n42"}}}, StopReason: model.StopReasonComplete},
	})
	value, err := agent.New(agent.Config{Name: "projector-test", Model: modelValue, ModelIdentity: model.Identity{Provider: "test", Model: "scripted"}, IDs: agenttest.SequentialIDs()})
	if err != nil {
		t.Fatal(err)
	}
	task := validTask("report", "agent")
	task.Output = ArtifactSpec{Name: "report.md", MediaType: "text/markdown"}
	input := NewArtifact("research.json", "application/json", []byte(`{"answer":42}`))
	artifact, err := (AgentSubagent{Agent: value}).Execute(context.Background(), task, []Artifact{input})
	if err != nil || string(artifact.Data) != "# Report\n\n42" {
		t.Fatalf("artifact = %#v, err = %v", artifact, err)
	}
}

func TestTextInputProjectorRejectsUnsafeInputs(t *testing.T) {
	task := validTask("one", "agent")
	large := NewArtifact("large.txt", "text/plain", []byte("12345"))
	if _, err := (TextInputProjector{MaxBytes: 4}).Project(task, []Artifact{large}); err == nil {
		t.Fatal("expected size limit error")
	}
	binary := NewArtifact("image.png", "image/png", []byte{1, 2, 3})
	if _, err := (TextInputProjector{}).Project(task, []Artifact{binary}); err == nil {
		t.Fatal("expected media type error")
	}
}

func TestFactoryFailureStopsBeforeExecution(t *testing.T) {
	brief := Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{validTask("one", "worker")}}
	for name, factory := range map[string]SubagentFactory{
		"nil":   factoryFunc(func(context.Context, string, TaskSpec) (Subagent, error) { return nil, nil }),
		"panic": factoryFunc(func(context.Context, string, TaskSpec) (Subagent, error) { panic("boom") }),
	} {
		t.Run(name, func(t *testing.T) {
			report, err := (Orchestrator{Factories: SubagentFactories{"worker": factory}}).Run(context.Background(), brief)
			if err == nil || report.Status != StatusFailed || report.Tasks[0].Status != TaskFailed {
				t.Fatalf("report = %#v, err = %v", report, err)
			}
		})
	}
}

func TestReadBriefRejectsUnknownAndTrailingJSON(t *testing.T) {
	valid := `{"id":"b","goal":"g","tasks":[{"id":"t","objective":"o","subagent":"s","output":{"name":"o.json","media_type":"application/json"}}]}`
	if _, err := ReadBrief(bytes.NewBufferString(valid + ` {}`)); err == nil {
		t.Fatal("expected trailing JSON error")
	}
	unknown := `{"id":"b","goal":"g","extra":true,"tasks":[{"id":"t","objective":"o","subagent":"s","output":{"name":"o.json","media_type":"application/json"}}]}`
	if _, err := ReadBrief(bytes.NewBufferString(unknown)); err == nil {
		t.Fatal("expected unknown field error")
	}
	brief, err := ReadBrief(bytes.NewBufferString(valid))
	if err != nil || brief.ID != "b" {
		t.Fatalf("brief = %#v, err = %v", brief, err)
	}
}

func TestRunWithSinkEmitsOrderedLifecycle(t *testing.T) {
	order := []string{}
	brief := Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{validTask("one", "worker"), validTask("two", "worker")}}
	o := Orchestrator{Subagents: Subagents{"worker": scriptedSubagent{order: &order, fn: func(task TaskSpec, _ []Artifact) (Artifact, error) {
		if task.ID == "one" {
			return Artifact{}, errors.New("failed")
		}
		return output(task, task.ID), nil
	}}}}
	var events []Event
	report, err := o.RunWithSink(context.Background(), brief, func(event Event) error {
		if validationErr := event.Validate(); validationErr != nil {
			t.Fatal(validationErr)
		}
		events = append(events, event)
		return nil
	})
	if err == nil || report.Status != StatusFailed {
		t.Fatalf("report = %#v, err = %v", report, err)
	}
	want := []EventType{EventStarted, EventTaskStarted, EventTaskFailed, EventTaskSkipped, EventFailed}
	if len(events) != len(want) {
		t.Fatalf("events = %#v", events)
	}
	for i, event := range events {
		if event.Type != want[i] || event.Sequence != int64(i+1) {
			t.Fatalf("event %d = %#v", i, event)
		}
	}
}

func TestRunWithSinkFailsClosed(t *testing.T) {
	order := []string{}
	brief := Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{validTask("one", "worker")}}
	o := Orchestrator{Subagents: Subagents{"worker": scriptedSubagent{order: &order, fn: func(task TaskSpec, _ []Artifact) (Artifact, error) {
		return output(task, task.ID), nil
	}}}}
	report, err := o.RunWithSink(context.Background(), brief, func(event Event) error {
		if event.Type == EventTaskStarted {
			return errors.New("sink unavailable")
		}
		return nil
	})
	if err == nil || report.Status != StatusFailed || len(order) != 0 || report.Tasks[0].Status != TaskFailed {
		t.Fatalf("report = %#v, order = %#v, err = %v", report, order, err)
	}
}

func TestRunWithSinkEmitsExactlyOneTerminalEvent(t *testing.T) {
	task := validTask("one", "worker")
	cases := []struct {
		name      string
		ctx       func() context.Context
		subagent  Subagent
		wantType  EventType
		wantError bool
	}{
		{name: "completed", ctx: context.Background, subagent: scriptedSubagent{order: new([]string), fn: func(task TaskSpec, _ []Artifact) (Artifact, error) { return output(task, task.ID), nil }}, wantType: EventCompleted},
		{name: "failed", ctx: context.Background, subagent: scriptedSubagent{order: new([]string), fn: func(TaskSpec, []Artifact) (Artifact, error) { return Artifact{}, errors.New("failed") }}, wantType: EventFailed, wantError: true},
		{name: "canceled", ctx: func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }, subagent: scriptedSubagent{order: new([]string), fn: func(task TaskSpec, _ []Artifact) (Artifact, error) { return output(task, task.ID), nil }}, wantType: EventCanceled, wantError: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var events []Event
			_, err := (Orchestrator{Subagents: Subagents{"worker": tc.subagent}}).RunWithSink(tc.ctx(), Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{task}}, func(event Event) error {
				events = append(events, event)
				return nil
			})
			if (err != nil) != tc.wantError {
				t.Fatalf("err = %v", err)
			}
			terminal := []Event{}
			for _, event := range events {
				if event.Type == EventCompleted || event.Type == EventFailed || event.Type == EventCanceled {
					terminal = append(terminal, event)
				}
			}
			if len(terminal) != 1 || terminal[0].Type != tc.wantType {
				t.Fatalf("events = %#v", events)
			}
		})
	}
}

func TestUnknownTaskVerifierFailsBeforeSubagent(t *testing.T) {
	task := validTask("one", "worker")
	task.Verification = &VerificationSpec{Verifier: "missing"}
	order := []string{}
	report, err := (Orchestrator{Subagents: Subagents{"worker": scriptedSubagent{order: &order, fn: func(task TaskSpec, _ []Artifact) (Artifact, error) { return output(task, task.ID), nil }}}}).Run(context.Background(), Brief{ID: "brief-1", Goal: "goal", Tasks: []TaskSpec{task}})
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Code != "verifier_unavailable" || len(order) != 0 || report.Tasks[0].Status != TaskVerificationFailed {
		t.Fatalf("report = %#v, order = %#v, run error = %#v", report, order, runErr)
	}
}
