package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/agent"
	"github.com/uyshero/tiggy-agent-runtime/agenttest"
	"github.com/uyshero/tiggy-agent-runtime/contextassembly"
	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/safespace"
	"github.com/uyshero/tiggy-agent-runtime/skill"
	"github.com/uyshero/tiggy-agent-runtime/tool"
	"github.com/uyshero/tiggy-agent-runtime/toolkit"
)

func TestAgentProjectsActivatedSkillInstructionsAndTools(t *testing.T) {
	t.Parallel()

	activation := activatedSkill(t, []string{"skill-read"})
	kit, err := toolkit.New()
	if err != nil {
		t.Fatal(err)
	}
	for _, registration := range []toolkit.Registration{
		{Tool: inertTool("read"), Groups: []string{"skill-read"}},
		{Tool: inertTool("write"), Groups: []string{"other"}},
	} {
		if err := kit.RegisterWith(registration); err != nil {
			t.Fatal(err)
		}
	}
	script := agenttest.NewScriptedModel(agenttest.ModelStep{
		Assert: func(request model.Request) error {
			if len(request.Messages) < 2 || request.Messages[0].Role != message.RoleSystem {
				return fmt.Errorf("messages = %#v", request.Messages)
			}
			prompt := request.Messages[0].Content[0].Text
			if !strings.Contains(prompt, "Base instructions") || !strings.Contains(prompt, "Use the read capability") {
				return fmt.Errorf("system prompt = %q", prompt)
			}
			if !strings.Contains(prompt, contextassembly.DynamicBoundary) {
				return fmt.Errorf("instruction stack boundary missing from system prompt: %q", prompt)
			}
			var metadata contextassembly.Metadata
			if err := json.Unmarshal(request.Messages[0].Metadata, &metadata); err != nil {
				return err
			}
			if metadata.Kind != contextassembly.MetadataKindInstructionStack || !metadata.DynamicBoundary {
				return fmt.Errorf("instruction stack metadata = %#v", metadata)
			}
			if len(request.Tools) != 1 || request.Tools[0].Name != "read" {
				return fmt.Errorf("projected tools = %#v", request.Tools)
			}
			return nil
		},
		Response: textResponse("done"),
	})
	value, err := agent.New(agent.Config{
		Name: "skill-agent", SystemPrompt: "Base instructions", Model: script,
		ModelIdentity: model.Identity{Provider: "test", Model: "scripted"}, Toolkit: kit,
		ToolGroups: []string{" skill-read "}, Skills: activation,
		IDs: agenttest.SequentialIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := value.Run(context.Background(), userMessage("review"))
	if err != nil || result.Status != agent.StatusCompleted {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
	var started event.RunStartedPayload
	if err := json.Unmarshal(result.Events[0].Payload, &started); err != nil {
		t.Fatal(err)
	}
	if len(started.Skills) != 1 || started.Skills[0].Name != "review" || !strings.HasPrefix(started.Skills[0].Digest, "sha256:") {
		t.Fatalf("run.started Skills = %#v", started.Skills)
	}
	if len(started.ToolGroups) != 1 || started.ToolGroups[0] != "skill-read" {
		t.Fatalf("run.started ToolGroups = %#v", started.ToolGroups)
	}
}

func TestActivatedSkillWithoutGroupsExposesNoTools(t *testing.T) {
	t.Parallel()

	activation := activatedSkill(t, nil)
	kit, err := toolkit.New(inertTool("global"))
	if err != nil {
		t.Fatal(err)
	}
	script := agenttest.NewScriptedModel(agenttest.ModelStep{
		Assert: func(request model.Request) error {
			if len(request.Tools) != 0 {
				return fmt.Errorf("projected tools = %#v", request.Tools)
			}
			return nil
		},
		Response: textResponse("done"),
	})
	value, err := agent.New(agent.Config{
		Name: "instruction-only", Model: script, ModelIdentity: model.Identity{Provider: "test", Model: "scripted"},
		Toolkit: kit, Skills: activation, IDs: agenttest.SequentialIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := value.Run(context.Background(), userMessage("review")); err != nil {
		t.Fatal(err)
	}
}

func TestAgentProjectsConfiguredInstructionSegments(t *testing.T) {
	t.Parallel()

	segments := []contextassembly.Segment{
		contextassembly.StaticSegment(contextassembly.KindProject, "repo/AGENTS.md", "Use repository conventions."),
		contextassembly.StaticSegment(contextassembly.KindTaskRule, "task", "Prefer focused changes."),
		contextassembly.StaticSegment(contextassembly.KindPolicy, "security", "Do not expose secrets."),
	}
	script := agenttest.NewScriptedModel(agenttest.ModelStep{
		Assert: func(request model.Request) error {
			if len(request.Messages) != 2 {
				return fmt.Errorf("messages = %#v", request.Messages)
			}
			prompt := request.Messages[0].Content[0].Text
			for _, text := range []string{"Base instructions", "Use repository conventions.", "Prefer focused changes.", "Do not expose secrets."} {
				if !strings.Contains(prompt, text) {
					return fmt.Errorf("instruction stack missing %q:\n%s", text, prompt)
				}
			}
			if strings.Contains(prompt, "mutated after New") {
				return fmt.Errorf("instruction stack used caller-mutated segment:\n%s", prompt)
			}
			var metadata contextassembly.Metadata
			if err := json.Unmarshal(request.Messages[0].Metadata, &metadata); err != nil {
				return err
			}
			wantKinds := []string{
				contextassembly.KindSystemPrompt,
				contextassembly.KindProject,
				contextassembly.KindTaskRule,
				contextassembly.KindPolicy,
			}
			if len(metadata.StaticKinds) != len(wantKinds) {
				return fmt.Errorf("static kinds = %#v", metadata.StaticKinds)
			}
			for index, want := range wantKinds {
				if metadata.StaticKinds[index] != want {
					return fmt.Errorf("static kinds = %#v", metadata.StaticKinds)
				}
			}
			return nil
		},
		Response: textResponse("done"),
	})
	value, err := agent.New(agent.Config{
		Name: "instruction-segments", SystemPrompt: "Base instructions", Instructions: segments,
		Model: script, ModelIdentity: model.Identity{Provider: "test", Model: "scripted"},
		IDs: agenttest.SequentialIDs(),
	})
	if err != nil {
		t.Fatal(err)
	}
	segments[1].Text = "mutated after New"
	result, err := value.Run(context.Background(), userMessage("review"))
	if err != nil || result.Status != agent.StatusCompleted {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
}

func activatedSkill(t *testing.T, groups []string) *skill.Activation {
	t.Helper()
	root := t.TempDir()
	directory := filepath.Join(root, "review")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := map[string]any{
		"schema": skill.SchemaV1, "name": "review", "version": "1", "description": "Review files.",
		"instructions": "SKILL.md", "tool_groups": groups,
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, skill.ManifestFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte("Use the read capability.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	space, err := safespace.OpenSafeSpace(root, safespace.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = space.Close() })
	catalog, err := skill.Discover(context.Background(), space, ".", skill.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	activation, err := catalog.Activate(context.Background(), skill.ActivationRequest{Name: "review"})
	if err != nil {
		t.Fatal(err)
	}
	return activation
}

func inertTool(name string) agenttest.FuncTool {
	return agenttest.FuncTool{
		DefinitionValue: model.ToolDefinition{Name: name, InputSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`)},
		ExecuteFunc:     func(context.Context, json.RawMessage) (tool.Result, error) { return tool.TextResult("unused"), nil },
	}
}
