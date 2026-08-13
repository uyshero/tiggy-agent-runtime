package skill_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/uyshero/tiggy-agent-runtime/safespace"
	"github.com/uyshero/tiggy-agent-runtime/skill"
)

func TestDiscoverAndExplicitActivation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeSkillFixture(t, root, "review", `{
  "schema": "tiggy.skill/v1",
  "name": "review",
  "version": "1.2.0",
  "description": "Review Go changes.",
  "instructions": "SKILL.md",
	  "tool_groups": ["write_tools", "read"],
  "resources": [{"name":"rules","path":"references/rules.md","description":"Project rules."}]
}`, "Inspect correctness before style.\n", map[string]string{"references/rules.md": "Never hide a runtime failure.\n"})

	space := openSpace(t, root)
	catalog, err := skill.Discover(context.Background(), space, ".", skill.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	descriptors := catalog.Descriptors()
	if len(descriptors) != 1 || descriptors[0].Name != "review" || descriptors[0].ManifestPath != "review/skill.json" {
		t.Fatalf("Descriptors() = %#v", descriptors)
	}
	if len(descriptors[0].ToolGroups) != 2 || descriptors[0].ToolGroups[0] != "read" {
		t.Fatalf("ToolGroups = %#v", descriptors[0].ToolGroups)
	}
	descriptors[0].ToolGroups[0] = "mutated"
	if catalog.Descriptors()[0].ToolGroups[0] != "read" {
		t.Fatal("Descriptors returned mutable Catalog state")
	}

	withoutResource, err := catalog.Activate(context.Background(), skill.ActivationRequest{Name: "review"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(withoutResource.SystemInstructions(), "Never hide") {
		t.Fatal("unselected resource entered system instructions")
	}

	activation, err := catalog.Activate(context.Background(), skill.ActivationRequest{Name: "review", Resources: []string{"rules"}})
	if err != nil {
		t.Fatal(err)
	}
	active := activation.Skills()
	if len(active) != 1 || active[0].Instructions != "Inspect correctness before style.\n" || len(active[0].Resources) != 1 {
		t.Fatalf("Skills() = %#v", active)
	}
	if !strings.HasPrefix(active[0].Digest, "sha256:") || len(active[0].Digest) != len("sha256:")+64 {
		t.Fatalf("Digest = %q", active[0].Digest)
	}
	if got := activation.ToolGroups(); len(got) != 2 || got[0] != "read" || got[1] != "write_tools" {
		t.Fatalf("ToolGroups() = %#v", got)
	}
	prompt := activation.SystemInstructions()
	for _, expected := range []string{"# Active Skills", "review @ 1.2.0", "Inspect correctness", "Resource: rules", "Never hide"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("system instructions do not contain %q: %s", expected, prompt)
		}
	}
	active[0].Resources[0].Content = "mutated"
	if activation.Skills()[0].Resources[0].Content == "mutated" {
		t.Fatal("Skills returned mutable Activation state")
	}
}

func TestDiscoveryRejectsInvalidManifests(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		manifest string
	}{
		{name: "unknown field", manifest: `{"schema":"tiggy.skill/v1","name":"bad","version":"1","description":"bad","instructions":"SKILL.md","extra":true}`},
		{name: "duplicate JSON key", manifest: `{"schema":"tiggy.skill/v1","name":"bad","name":"bad","version":"1","description":"bad","instructions":"SKILL.md"}`},
		{name: "nested duplicate JSON key", manifest: `{"schema":"tiggy.skill/v1","name":"bad","version":"1","description":"bad","instructions":"SKILL.md","resources":[{"name":"one","name":"two","path":"a.md"}]}`},
		{name: "directory mismatch", manifest: `{"schema":"tiggy.skill/v1","name":"other","version":"1","description":"bad","instructions":"SKILL.md"}`},
		{name: "path traversal", manifest: `{"schema":"tiggy.skill/v1","name":"bad","version":"1","description":"bad","instructions":"../outside.md"}`},
		{name: "duplicate groups", manifest: `{"schema":"tiggy.skill/v1","name":"bad","version":"1","description":"bad","instructions":"SKILL.md","tool_groups":["read","read"]}`},
		{name: "duplicate resource paths", manifest: `{"schema":"tiggy.skill/v1","name":"bad","version":"1","description":"bad","instructions":"SKILL.md","resources":[{"name":"one","path":"a.md"},{"name":"two","path":"a.md"}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeSkillFixture(t, root, "bad", test.manifest, "bad\n", nil)
			_, err := skill.Discover(context.Background(), openSpace(t, root), ".", skill.Limits{})
			assertSkillCode(t, err, skill.ErrorInvalidManifest)
		})
	}
}

func TestActivationRejectsUnknownDuplicateAndInvalidContent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeSkillFixture(t, root, "bounded", `{
  "schema":"tiggy.skill/v1","name":"bounded","version":"1","description":"bounded",
  "instructions":"SKILL.md","resources":[{"name":"binary","path":"binary.dat"}]
}`, "12345", nil)
	if err := os.WriteFile(filepath.Join(root, "bounded", "binary.dat"), []byte{0xff, 0xfe}, 0o600); err != nil {
		t.Fatal(err)
	}
	catalog, err := skill.Discover(context.Background(), openSpace(t, root), ".", skill.Limits{MaxInstructionBytes: 4})
	if err != nil {
		t.Fatal(err)
	}
	_, err = catalog.Activate(context.Background(), skill.ActivationRequest{Name: "missing"})
	assertSkillCode(t, err, skill.ErrorSkillNotFound)
	_, err = catalog.Activate(context.Background(), skill.ActivationRequest{Name: "bounded"}, skill.ActivationRequest{Name: "bounded"})
	assertSkillCode(t, err, skill.ErrorInvalidConfig)
	_, err = catalog.Activate(context.Background(), skill.ActivationRequest{Name: "bounded"})
	assertSkillCode(t, err, skill.ErrorActivationLimit)

	catalog, err = skill.Discover(context.Background(), openSpace(t, root), ".", skill.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = catalog.Activate(context.Background(), skill.ActivationRequest{Name: "bounded", Resources: []string{"unknown"}})
	assertSkillCode(t, err, skill.ErrorResourceMissing)
	_, err = catalog.Activate(context.Background(), skill.ActivationRequest{Name: "bounded", Resources: []string{"binary"}})
	assertSkillCode(t, err, skill.ErrorInvalidContent)
}

func TestDiscoveryPropagatesCancellationAndContainsSourcePanic(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := skill.Discover(ctx, panicReader{}, ".", skill.Limits{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled discovery error = %v", err)
	}
	_, err = skill.Discover(context.Background(), panicReader{}, ".", skill.Limits{})
	assertSkillCode(t, err, skill.ErrorSourceFailed)
}

type panicReader struct{}

func (panicReader) ReadFile(context.Context, string) ([]byte, error) { panic("read panic") }
func (panicReader) List(context.Context, string) ([]safespace.Entry, error) {
	panic("list panic")
}

func writeSkillFixture(t *testing.T, root, name, manifest, instructions string, resources map[string]string) {
	t.Helper()
	directory := filepath.Join(root, name)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"skill.json": manifest, "SKILL.md": instructions} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range resources {
		file := filepath.Join(directory, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func openSpace(t *testing.T, root string) *safespace.SafeSpace {
	t.Helper()
	value, err := safespace.OpenSafeSpace(root, safespace.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = value.Close() })
	return value
}

func assertSkillCode(t *testing.T, err error, code skill.ErrorCode) {
	t.Helper()
	var value *skill.Error
	if !errors.As(err, &value) || value.Code != code {
		t.Fatalf("error = %v, want Skill code %q", err, code)
	}
}
