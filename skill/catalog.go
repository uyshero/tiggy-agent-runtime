package skill

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"runtime/debug"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/uyshero/tiggy-agent-runtime/safespace"
)

const (
	defaultMaxSkills            = 128
	defaultMaxManifestBytes     = 64 << 10
	defaultMaxInstructionBytes  = 128 << 10
	defaultMaxResourcesPerSkill = 32
	defaultMaxResourceBytes     = 256 << 10
	defaultMaxActivationBytes   = 512 << 10
)

// Limits 限制目录扫描与激活时可读入的 Skill 体积。
type Limits struct {
	MaxSkills            int
	MaxManifestBytes     int64
	MaxInstructionBytes  int64
	MaxResourcesPerSkill int
	MaxResourceBytes     int64
	MaxActivationBytes   int64
}

// DefaultLimits 返回 Skill Catalog 的标准边界。
func DefaultLimits() Limits {
	return Limits{
		MaxSkills: defaultMaxSkills, MaxManifestBytes: defaultMaxManifestBytes,
		MaxInstructionBytes: defaultMaxInstructionBytes, MaxResourcesPerSkill: defaultMaxResourcesPerSkill,
		MaxResourceBytes: defaultMaxResourceBytes, MaxActivationBytes: defaultMaxActivationBytes,
	}
}

func normalizeLimits(value Limits) (Limits, error) {
	defaults := DefaultLimits()
	if value.MaxSkills == 0 {
		value.MaxSkills = defaults.MaxSkills
	}
	if value.MaxManifestBytes == 0 {
		value.MaxManifestBytes = defaults.MaxManifestBytes
	}
	if value.MaxInstructionBytes == 0 {
		value.MaxInstructionBytes = defaults.MaxInstructionBytes
	}
	if value.MaxResourcesPerSkill == 0 {
		value.MaxResourcesPerSkill = defaults.MaxResourcesPerSkill
	}
	if value.MaxResourceBytes == 0 {
		value.MaxResourceBytes = defaults.MaxResourceBytes
	}
	if value.MaxActivationBytes == 0 {
		value.MaxActivationBytes = defaults.MaxActivationBytes
	}
	if value.MaxSkills < 0 || value.MaxManifestBytes < 0 || value.MaxInstructionBytes < 0 || value.MaxResourcesPerSkill < 0 || value.MaxResourceBytes < 0 || value.MaxActivationBytes < 0 {
		return Limits{}, fmt.Errorf("skill limits must be positive")
	}
	return value, nil
}

type catalogSkill struct {
	descriptor      Descriptor
	directory       string
	instructionPath string
	resources       map[string]ResourceDescriptor
}

// Catalog is an immutable metadata snapshot backed by a caller-owned Reader.
// Activate reads and snapshots only explicitly selected content.
type Catalog struct {
	source safespace.Reader
	limits Limits
	values map[string]catalogSkill
	list   []Descriptor
}

// Discover indexes direct child directories under root. Every child directory
// is a Skill directory and must contain a valid skill.json manifest.
func Discover(ctx context.Context, source safespace.Reader, root string, limits Limits) (*Catalog, error) {
	if ctx == nil || source == nil {
		return nil, &Error{Code: ErrorInvalidConfig, Operation: "discover", Cause: fmt.Errorf("context and source are required")}
	}
	limits, err := normalizeLimits(limits)
	if err != nil {
		return nil, &Error{Code: ErrorInvalidConfig, Operation: "discover", Cause: err}
	}
	root, err = safespace.NormalizePath(root)
	if err != nil {
		return nil, &Error{Code: ErrorInvalidConfig, Operation: "discover", Path: root, Cause: err}
	}
	entries, err := sourceList(ctx, source, root)
	if err != nil {
		return nil, sourceError(ctx, "discover", "", root, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(entries, func(first, second int) bool { return entries[first].Name < entries[second].Name })
	values := make(map[string]catalogSkill)
	for _, entry := range entries {
		if !entry.IsDir {
			continue
		}
		if len(values) >= limits.MaxSkills {
			return nil, &Error{Code: ErrorCatalogLimit, Operation: "discover", Cause: fmt.Errorf("skill count exceeds %d", limits.MaxSkills)}
		}
		directory, err := safespace.NormalizePath(path.Join(root, entry.Name))
		if err != nil {
			return nil, &Error{Code: ErrorInvalidManifest, Operation: "discover", Path: entry.Path, Cause: err}
		}
		manifestPath := path.Join(directory, ManifestFile)
		data, err := sourceRead(ctx, source, manifestPath)
		if err != nil {
			return nil, sourceError(ctx, "discover", entry.Name, manifestPath, err)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if int64(len(data)) > limits.MaxManifestBytes {
			return nil, &Error{Code: ErrorCatalogLimit, Operation: "discover", Skill: entry.Name, Path: manifestPath, Cause: fmt.Errorf("manifest exceeds %d bytes", limits.MaxManifestBytes)}
		}
		manifest, err := decodeManifest(data)
		if err != nil {
			return nil, &Error{Code: ErrorInvalidManifest, Operation: "discover", Skill: entry.Name, Path: manifestPath, Cause: err}
		}
		value, err := validateManifest(manifest, directory, entry.Name, manifestPath, limits)
		if err != nil {
			return nil, &Error{Code: ErrorInvalidManifest, Operation: "discover", Skill: entry.Name, Path: manifestPath, Cause: err}
		}
		if _, exists := values[value.descriptor.Name]; exists {
			return nil, &Error{Code: ErrorInvalidManifest, Operation: "discover", Skill: value.descriptor.Name, Path: manifestPath, Cause: fmt.Errorf("duplicate skill name")}
		}
		values[value.descriptor.Name] = value
	}
	list := make([]Descriptor, 0, len(values))
	for _, value := range values {
		list = append(list, cloneDescriptor(value.descriptor))
	}
	sort.Slice(list, func(first, second int) bool { return list[first].Name < list[second].Name })
	return &Catalog{source: source, limits: limits, values: values, list: list}, nil
}

func (c *Catalog) Descriptors() []Descriptor {
	if c == nil {
		return nil
	}
	result := make([]Descriptor, len(c.list))
	for index, value := range c.list {
		result[index] = cloneDescriptor(value)
	}
	return result
}

func decodeManifest(data []byte) (Manifest, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return Manifest{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var value Manifest
	if err := decoder.Decode(&value); err != nil {
		return Manifest{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("manifest contains multiple JSON values")
		}
		return Manifest{}, err
	}
	return value, nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("manifest contains multiple JSON values")
		}
		return err
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("manifest object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("manifest contains duplicate key %q", key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("invalid manifest object: %w", err)
		}
		if closing != json.Delim('}') {
			return fmt.Errorf("invalid manifest object delimiter %q", closing)
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("invalid manifest array: %w", err)
		}
		if closing != json.Delim(']') {
			return fmt.Errorf("invalid manifest array delimiter %q", closing)
		}
	default:
		return fmt.Errorf("invalid manifest delimiter %q", delimiter)
	}
	return nil
}

func validateManifest(manifest Manifest, directory, directoryName, manifestPath string, limits Limits) (catalogSkill, error) {
	if manifest.Schema != SchemaV1 {
		return catalogSkill{}, fmt.Errorf("schema must be %q", SchemaV1)
	}
	if err := validateName("skill name", manifest.Name, 64, true); err != nil {
		return catalogSkill{}, err
	}
	if manifest.Name != directoryName {
		return catalogSkill{}, fmt.Errorf("skill name %q must match directory %q", manifest.Name, directoryName)
	}
	if err := validateVersion(manifest.Version); err != nil {
		return catalogSkill{}, err
	}
	description, err := boundedText("description", manifest.Description, 1024, false)
	if err != nil {
		return catalogSkill{}, err
	}
	instructionPath, err := relativeContentPath(manifest.Instructions)
	if err != nil {
		return catalogSkill{}, fmt.Errorf("instructions: %w", err)
	}
	groups := make([]string, 0, len(manifest.ToolGroups))
	seenGroups := make(map[string]struct{})
	for _, group := range manifest.ToolGroups {
		if err := validateQualifiedName("tool group", group, 64); err != nil {
			return catalogSkill{}, err
		}
		if _, exists := seenGroups[group]; exists {
			return catalogSkill{}, fmt.Errorf("duplicate tool group %q", group)
		}
		seenGroups[group] = struct{}{}
		groups = append(groups, group)
	}
	sort.Strings(groups)
	if len(manifest.Resources) > limits.MaxResourcesPerSkill {
		return catalogSkill{}, fmt.Errorf("resource count exceeds %d", limits.MaxResourcesPerSkill)
	}
	resources := make(map[string]ResourceDescriptor, len(manifest.Resources))
	resourceList := make([]ResourceDescriptor, 0, len(manifest.Resources))
	seenPaths := map[string]struct{}{instructionPath: {}}
	for _, resource := range manifest.Resources {
		if err := validateName("resource name", resource.Name, 64, true); err != nil {
			return catalogSkill{}, err
		}
		resourcePath, err := relativeContentPath(resource.Path)
		if err != nil {
			return catalogSkill{}, fmt.Errorf("resource %q: %w", resource.Name, err)
		}
		resourceDescription := ""
		if resource.Description != "" {
			resourceDescription, err = boundedText("resource description", resource.Description, 1024, false)
			if err != nil {
				return catalogSkill{}, err
			}
		}
		if _, exists := resources[resource.Name]; exists {
			return catalogSkill{}, fmt.Errorf("duplicate resource %q", resource.Name)
		}
		if _, exists := seenPaths[resourcePath]; exists {
			return catalogSkill{}, fmt.Errorf("resource path %q is duplicated", resourcePath)
		}
		seenPaths[resourcePath] = struct{}{}
		descriptor := ResourceDescriptor{Name: resource.Name, Path: resourcePath, Description: resourceDescription}
		resources[resource.Name] = descriptor
		resourceList = append(resourceList, descriptor)
	}
	sort.Slice(resourceList, func(first, second int) bool { return resourceList[first].Name < resourceList[second].Name })
	descriptor := Descriptor{
		Name: manifest.Name, Version: manifest.Version, Description: description,
		ToolGroups: groups, Resources: resourceList, ManifestPath: manifestPath,
	}
	return catalogSkill{
		descriptor: descriptor, directory: directory,
		instructionPath: instructionPath, resources: resources,
	}, nil
}

func validateName(label, value string, maximum int, lowercase bool) error {
	if value == "" || len(value) > maximum || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must be non-empty and at most %d bytes", label, maximum)
	}
	for index, char := range []byte(value) {
		allowed := char >= 'a' && char <= 'z' || !lowercase && char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-'
		if !allowed || (index == 0 || index == len(value)-1) && char == '-' {
			return fmt.Errorf("%s %q is invalid", label, value)
		}
	}
	return nil
}

func validateVersion(value string) error {
	if value == "" || len(value) > 64 || strings.TrimSpace(value) != value {
		return fmt.Errorf("skill version must be non-empty and at most 64 bytes")
	}
	for index, char := range []byte(value) {
		allowed := char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("._+-", rune(char))
		if !allowed || index == 0 && !isASCIIAlphanumeric(char) {
			return fmt.Errorf("skill version %q is invalid", value)
		}
	}
	return nil
}

func validateQualifiedName(label, value string, maximum int) error {
	if value == "" || len(value) > maximum || strings.TrimSpace(value) != value {
		return fmt.Errorf("%s must be non-empty and at most %d bytes", label, maximum)
	}
	for _, segment := range strings.Split(value, ".") {
		if segment == "" {
			return fmt.Errorf("%s %q contains an empty segment", label, value)
		}
		for index, char := range []byte(segment) {
			allowed := isASCIIAlphanumeric(char) || char == '_' || char == '-'
			if !allowed || index == 0 && char == '-' {
				return fmt.Errorf("%s %q contains an unsupported character", label, value)
			}
		}
	}
	return nil
}

func isASCIIAlphanumeric(char byte) bool {
	return char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9'
}

func relativeContentPath(value string) (string, error) {
	if value == "" || strings.Contains(value, "\\") {
		return "", fmt.Errorf("path must use non-empty forward-slash form")
	}
	normalized, err := safespace.NormalizePath(value)
	if err != nil || normalized == "." {
		return "", fmt.Errorf("invalid relative path: %w", err)
	}
	if normalized != value {
		return "", fmt.Errorf("path %q is not canonical", value)
	}
	return normalized, nil
}

func boundedText(label, value string, maximum int64, allowEmpty bool) (string, error) {
	if !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("%s must be UTF-8 text without null bytes", label)
	}
	if !allowEmpty && strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	if int64(len(value)) > maximum {
		return "", fmt.Errorf("%s exceeds %d bytes", label, maximum)
	}
	return value, nil
}

func sourceRead(ctx context.Context, source safespace.Reader, file string) (data []byte, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("skill source read panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return source.ReadFile(ctx, file)
}

func sourceList(ctx context.Context, source safespace.Reader, directory string) (entries []safespace.Entry, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("skill source list panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return source.List(ctx, directory)
}

func sourceError(ctx context.Context, operation, skillName, file string, cause error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		return cause
	}
	return &Error{Code: ErrorSourceFailed, Operation: operation, Skill: skillName, Path: file, Cause: cause}
}

func contentDigest(descriptor Descriptor, instructions string, resources []ActivatedResource) string {
	hash := sha256.New()
	_, _ = hash.Write(descriptorJSON(descriptor))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(instructions))
	for _, resource := range resources {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(resource.Name))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(resource.Content))
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}
