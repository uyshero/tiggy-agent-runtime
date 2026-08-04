package skill

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
)

// Activate creates one immutable snapshot. Skill and Resource selection is
// explicit; undeclared resources and duplicate requests are rejected.
func (c *Catalog) Activate(ctx context.Context, requests ...ActivationRequest) (*Activation, error) {
	if ctx == nil || c == nil {
		return nil, &Error{Code: ErrorInvalidConfig, Operation: "activate", Cause: fmt.Errorf("context and catalog are required")}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	seenSkills := make(map[string]struct{}, len(requests))
	for _, request := range requests {
		if _, exists := seenSkills[request.Name]; exists {
			return nil, &Error{Code: ErrorInvalidConfig, Operation: "activate", Skill: request.Name, Cause: fmt.Errorf("duplicate activation request")}
		}
		seenSkills[request.Name] = struct{}{}
		value, exists := c.values[request.Name]
		if !exists {
			return nil, &Error{Code: ErrorSkillNotFound, Operation: "activate", Skill: request.Name}
		}
		seenResources := make(map[string]struct{}, len(request.Resources))
		for _, resourceName := range request.Resources {
			if _, exists := seenResources[resourceName]; exists {
				return nil, &Error{Code: ErrorInvalidConfig, Operation: "activate", Skill: request.Name, Cause: fmt.Errorf("duplicate resource %q", resourceName)}
			}
			seenResources[resourceName] = struct{}{}
			if _, exists := value.resources[resourceName]; !exists {
				return nil, &Error{Code: ErrorResourceMissing, Operation: "activate", Skill: request.Name, Cause: fmt.Errorf("resource %q is not declared", resourceName)}
			}
		}
	}
	active := make([]ActivatedSkill, 0, len(requests))
	groups := make(map[string]struct{})
	var contentBytes int64
	for _, request := range requests {
		value := c.values[request.Name]
		instructions, err := c.readText(ctx, value.descriptor.Name, path.Join(value.directory, value.instructionPath), c.limits.MaxInstructionBytes, "instructions")
		if err != nil {
			return nil, err
		}
		contentBytes += int64(len(instructions))
		selected := make([]ActivatedResource, 0, len(request.Resources))
		for _, resourceName := range request.Resources {
			resource := value.resources[resourceName]
			content, err := c.readText(ctx, value.descriptor.Name, path.Join(value.directory, resource.Path), c.limits.MaxResourceBytes, "resource")
			if err != nil {
				return nil, err
			}
			contentBytes += int64(len(content))
			if contentBytes > c.limits.MaxActivationBytes {
				return nil, &Error{Code: ErrorActivationLimit, Operation: "activate", Skill: request.Name, Cause: fmt.Errorf("activated content exceeds %d bytes", c.limits.MaxActivationBytes)}
			}
			selected = append(selected, ActivatedResource{Name: resource.Name, Description: resource.Description, Content: content})
		}
		for _, group := range value.descriptor.ToolGroups {
			groups[group] = struct{}{}
		}
		activated := ActivatedSkill{
			Name: value.descriptor.Name, Version: value.descriptor.Version, Description: value.descriptor.Description,
			Instructions: instructions, ToolGroups: append([]string(nil), value.descriptor.ToolGroups...), Resources: selected,
		}
		activated.Digest = contentDigest(value.descriptor, instructions, selected)
		active = append(active, activated)
	}
	toolGroups := make([]string, 0, len(groups))
	for group := range groups {
		toolGroups = append(toolGroups, group)
	}
	sort.Strings(toolGroups)
	prompt := renderPrompt(active)
	if int64(len(prompt)) > c.limits.MaxActivationBytes {
		return nil, &Error{Code: ErrorActivationLimit, Operation: "activate", Cause: fmt.Errorf("rendered activation exceeds %d bytes", c.limits.MaxActivationBytes)}
	}
	return &Activation{skills: active, toolGroups: toolGroups, prompt: prompt}, nil
}

func (c *Catalog) readText(ctx context.Context, skillName, file string, maximum int64, label string) (string, error) {
	data, err := sourceRead(ctx, c.source, file)
	if err != nil {
		return "", sourceError(ctx, "activate", skillName, file, err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	value, err := boundedText(label, string(data), maximum, false)
	if err != nil {
		code := ErrorInvalidContent
		if int64(len(data)) > maximum {
			code = ErrorActivationLimit
		}
		return "", &Error{Code: code, Operation: "activate", Skill: skillName, Path: file, Cause: err}
	}
	return value, nil
}

func renderPrompt(skills []ActivatedSkill) string {
	if len(skills) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("# Active Skills\n")
	for _, value := range skills {
		builder.WriteString("\n## Skill: ")
		builder.WriteString(value.Name)
		builder.WriteString(" @ ")
		builder.WriteString(value.Version)
		builder.WriteString("\n\n")
		builder.WriteString(value.Instructions)
		if !strings.HasSuffix(value.Instructions, "\n") {
			builder.WriteByte('\n')
		}
		for _, resource := range value.Resources {
			builder.WriteString("\n### Resource: ")
			builder.WriteString(resource.Name)
			builder.WriteString("\n\n")
			builder.WriteString(resource.Content)
			if !strings.HasSuffix(resource.Content, "\n") {
				builder.WriteByte('\n')
			}
		}
	}
	return builder.String()
}
