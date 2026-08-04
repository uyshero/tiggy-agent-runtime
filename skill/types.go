// Package skill discovers and explicitly activates bounded, versioned Agent
// instruction resources. Skills contain trusted text and capability metadata;
// they do not execute code or acquire tools by themselves.
package skill

import "encoding/json"

const (
	SchemaV1     = "tiggy.skill/v1"
	ManifestFile = "skill.json"
)

// Manifest is the strict on-disk metadata format. Paths are relative to the
// directory containing skill.json.
type Manifest struct {
	Schema       string             `json:"schema"`
	Name         string             `json:"name"`
	Version      string             `json:"version"`
	Description  string             `json:"description"`
	Instructions string             `json:"instructions"`
	ToolGroups   []string           `json:"tool_groups,omitempty"`
	Resources    []ResourceManifest `json:"resources,omitempty"`
}

type ResourceManifest struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}

// Descriptor is immutable discovery metadata returned without instruction or
// resource content.
type Descriptor struct {
	Name         string               `json:"name"`
	Version      string               `json:"version"`
	Description  string               `json:"description"`
	ToolGroups   []string             `json:"tool_groups,omitempty"`
	Resources    []ResourceDescriptor `json:"resources,omitempty"`
	ManifestPath string               `json:"manifest_path"`
}

type ResourceDescriptor struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Description string `json:"description,omitempty"`
}

// ActivationRequest explicitly selects one discovered Skill and the declared
// resources whose content should enter the model's system instructions.
type ActivationRequest struct {
	Name      string
	Resources []string
}

type ActivatedResource struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Content     string `json:"content"`
}

type ActivatedSkill struct {
	Name         string              `json:"name"`
	Version      string              `json:"version"`
	Description  string              `json:"description"`
	Digest       string              `json:"digest"`
	Instructions string              `json:"instructions"`
	ToolGroups   []string            `json:"tool_groups,omitempty"`
	Resources    []ActivatedResource `json:"resources,omitempty"`
}

// Activation is an immutable snapshot. Its zero value means no active Skills.
type Activation struct {
	skills     []ActivatedSkill
	toolGroups []string
	prompt     string
}

func (a *Activation) Empty() bool {
	return a == nil || len(a.skills) == 0
}

func (a *Activation) Skills() []ActivatedSkill {
	if a == nil {
		return nil
	}
	return cloneActivatedSkills(a.skills)
}

func (a *Activation) ToolGroups() []string {
	if a == nil {
		return nil
	}
	return append([]string(nil), a.toolGroups...)
}

// SystemInstructions returns the bounded deterministic text to append to the
// Agent system prompt.
func (a *Activation) SystemInstructions() string {
	if a == nil {
		return ""
	}
	return a.prompt
}

func (a *Activation) Clone() *Activation {
	if a == nil {
		return nil
	}
	return &Activation{
		skills: cloneActivatedSkills(a.skills), toolGroups: append([]string(nil), a.toolGroups...), prompt: a.prompt,
	}
}

func cloneDescriptor(value Descriptor) Descriptor {
	value.ToolGroups = append([]string(nil), value.ToolGroups...)
	value.Resources = append([]ResourceDescriptor(nil), value.Resources...)
	return value
}

func cloneActivatedSkills(values []ActivatedSkill) []ActivatedSkill {
	result := make([]ActivatedSkill, len(values))
	for index, value := range values {
		value.ToolGroups = append([]string(nil), value.ToolGroups...)
		value.Resources = append([]ActivatedResource(nil), value.Resources...)
		result[index] = value
	}
	return result
}

func descriptorJSON(value Descriptor) []byte {
	data, _ := json.Marshal(value)
	return data
}
