package contextassembly_test

import (
	"strings"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/contextassembly"
)

func TestRenderSeparatesStaticInstructionsFromDynamicContext(t *testing.T) {
	t.Parallel()

	prompt, metadata, err := contextassembly.Render([]contextassembly.Segment{
		contextassembly.StaticSegment(contextassembly.KindSystemPrompt, "config", "Follow runtime rules."),
		contextassembly.StaticSegment(contextassembly.KindSkill, "skill.review", "Review only changed files."),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "# Instruction Stack") ||
		!strings.Contains(prompt, "## Static Instructions") ||
		!strings.Contains(prompt, contextassembly.DynamicBoundary) ||
		!strings.Contains(prompt, "## Dynamic Context") {
		t.Fatalf("prompt did not render expected sections:\n%s", prompt)
	}
	staticIndex := strings.Index(prompt, "Follow runtime rules.")
	boundaryIndex := strings.Index(prompt, contextassembly.DynamicBoundary)
	dynamicIndex := strings.Index(prompt, "Runtime may insert request-only evidence")
	if staticIndex < 0 || boundaryIndex <= staticIndex || dynamicIndex <= boundaryIndex {
		t.Fatalf("unexpected instruction ordering:\n%s", prompt)
	}
	if metadata.Kind != contextassembly.MetadataKindInstructionStack || !metadata.DynamicBoundary {
		t.Fatalf("metadata = %#v", metadata)
	}
	if len(metadata.StaticKinds) != 2 || metadata.StaticKinds[0] != contextassembly.KindSystemPrompt || metadata.StaticKinds[1] != contextassembly.KindSkill {
		t.Fatalf("static kinds = %#v", metadata.StaticKinds)
	}
	if len(metadata.StaticKindBytes) != 2 ||
		metadata.StaticKindBytes[0].Kind != contextassembly.KindSystemPrompt || metadata.StaticKindBytes[0].Bytes != int64(len("Follow runtime rules.")) ||
		metadata.StaticKindBytes[1].Kind != contextassembly.KindSkill || metadata.StaticKindBytes[1].Bytes != int64(len("Review only changed files.")) {
		t.Fatalf("static kind bytes = %#v", metadata.StaticKindBytes)
	}
}

func TestRenderKeepsConfiguredStaticSegmentOrder(t *testing.T) {
	t.Parallel()

	prompt, metadata, err := contextassembly.Render([]contextassembly.Segment{
		contextassembly.StaticSegment(contextassembly.KindSystemPrompt, "config", "Base."),
		contextassembly.StaticSegment(contextassembly.KindProject, "project", "Project conventions."),
		contextassembly.StaticSegment(contextassembly.KindTaskRule, "task", "Task rule."),
		contextassembly.StaticSegment(contextassembly.KindPolicy, "policy", "Policy rule."),
		contextassembly.StaticSegment(contextassembly.KindKnowledge, "knowledge", "Domain knowledge."),
		contextassembly.StaticSegment(contextassembly.KindSkill, "skill", "Skill rule."),
	})
	if err != nil {
		t.Fatal(err)
	}
	previous := -1
	for _, text := range []string{"Base.", "Project conventions.", "Task rule.", "Policy rule.", "Domain knowledge.", "Skill rule."} {
		index := strings.Index(prompt, text)
		if index <= previous {
			t.Fatalf("segment %q rendered out of order:\n%s", text, prompt)
		}
		previous = index
	}
	if len(metadata.StaticKinds) != 6 {
		t.Fatalf("static kinds = %#v", metadata.StaticKinds)
	}
	if len(metadata.StaticKindBytes) != 6 {
		t.Fatalf("static kind bytes = %#v", metadata.StaticKindBytes)
	}
}

func TestRenderAggregatesStaticKindBytes(t *testing.T) {
	t.Parallel()

	_, metadata, err := contextassembly.Render([]contextassembly.Segment{
		contextassembly.StaticSegment(contextassembly.KindKnowledge, "knowledge.a", "Alpha"),
		contextassembly.StaticSegment(contextassembly.KindKnowledge, "knowledge.b", "Beta"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(metadata.StaticKinds) != 1 || metadata.StaticKinds[0] != contextassembly.KindKnowledge {
		t.Fatalf("static kinds = %#v", metadata.StaticKinds)
	}
	if len(metadata.StaticKindBytes) != 1 || metadata.StaticKindBytes[0].Kind != contextassembly.KindKnowledge || metadata.StaticKindBytes[0].Bytes != int64(len("Alpha")+len("Beta")) {
		t.Fatalf("static kind bytes = %#v", metadata.StaticKindBytes)
	}
}

func TestRenderSkipsEmptySegments(t *testing.T) {
	t.Parallel()

	prompt, metadata, err := contextassembly.Render([]contextassembly.Segment{
		contextassembly.StaticSegment(contextassembly.KindSystemPrompt, "config", " "),
	})
	if err != nil {
		t.Fatal(err)
	}
	if prompt != "" || metadata.Kind != "" {
		t.Fatalf("Render() = %q, %#v; want empty", prompt, metadata)
	}
}

func TestRenderRejectsUnsupportedDynamicSegmentInput(t *testing.T) {
	t.Parallel()

	_, _, err := contextassembly.Render([]contextassembly.Segment{{
		Layer: contextassembly.LayerDynamic,
		Kind:  "memory",
		Text:  "remembered evidence",
	}})
	if err == nil {
		t.Fatal("Render accepted dynamic input inside the static instruction stack")
	}
}
