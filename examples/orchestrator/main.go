package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/uyshero/tiggy-agent-runtime/agent"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/orchestrator"
)

func main() {
	brief := orchestrator.Brief{ID: "release-brief", Goal: "prepare a release report", Tasks: []orchestrator.TaskSpec{
		{ID: "research", Objective: "Return release facts as JSON.", Subagent: "agent", Output: orchestrator.ArtifactSpec{Name: "research.json", MediaType: "application/json"}, Verification: &orchestrator.VerificationSpec{Verifier: orchestrator.VerifierJSONSchema, Config: json.RawMessage(`{"type":"object","required":["version","changes"],"properties":{"version":{"type":"string"},"changes":{"type":"array","items":{"type":"string"}}}}`)}},
		{ID: "write", Objective: "Write a Markdown report from the research.", Subagent: "agent", InputArtifacts: []orchestrator.ArtifactInput{{TaskID: "research"}}, Output: orchestrator.ArtifactSpec{Name: "report.md", MediaType: "text/markdown"}, Verification: &orchestrator.VerificationSpec{Verifier: orchestrator.VerifierText, Config: json.RawMessage(`{"prefix":"# Release","contains":["Verified artifacts"]}`)}},
	}}

	store, err := orchestrator.NewInMemoryStore(orchestrator.InMemoryStoreOptions{})
	if err != nil {
		log.Fatal(err)
	}
	checkpoints, err := orchestrator.NewInMemoryCheckpointStore(orchestrator.InMemoryCheckpointOptions{})
	if err != nil {
		log.Fatal(err)
	}
	runtime := orchestrator.Orchestrator{
		Factories:   orchestrator.SubagentFactories{"agent": taskAgentFactory{}},
		Verifiers:   orchestrator.BuiltinVerifiers(),
		Store:       store,
		Checkpoints: checkpoints,
	}
	report, err := runtime.RunWithSink(context.Background(), brief, func(event orchestrator.Event) error {
		fmt.Printf("%02d  %-28s %s\n", event.Sequence, event.Type, event.TaskID)
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
	final, err := store.Get(context.Background(), report.ArtifactReferences[1])
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("\nexecution: %s (checkpoint v%d)\n%s\n", report.ExecutionID, report.CheckpointVersion, final.Data)
}

type taskAgentFactory struct{}

func (taskAgentFactory) Create(_ context.Context, executionID string, task orchestrator.TaskSpec) (orchestrator.Subagent, error) {
	modelValue := taskModel{taskID: task.ID}
	value, err := agent.New(agent.Config{
		Name:  task.ID + "-" + executionID,
		Model: modelValue, ModelIdentity: model.Identity{Provider: "example", Model: "deterministic-orchestrator"},
	})
	if err != nil {
		return nil, err
	}
	return orchestrator.AgentSubagent{Agent: value}, nil
}

type taskModel struct{ taskID string }

func (m taskModel) Generate(_ context.Context, request model.Request, _ model.DeltaSink) (model.Response, error) {
	text := request.Messages[len(request.Messages)-1].Content[0].Text
	output := `{"version":"1.2.0","changes":["verified artifacts","isolated task agents"]}`
	if m.taskID == "write" {
		if !strings.Contains(text, `\"version\":\"1.2.0\"`) {
			return model.Response{}, fmt.Errorf("research content was not projected")
		}
		output = "# Release 1.2.0\n\n- Verified artifacts\n- Isolated task agents"
	}
	return model.Response{Message: message.Message{Content: []message.Content{{Type: message.ContentText, Text: output}}}, StopReason: model.StopReasonComplete}, nil
}
