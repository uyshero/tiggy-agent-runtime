package builtin

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/uyshero/tiggy-agent-runtime/safespace"
	"github.com/uyshero/tiggy-agent-runtime/tool"
)

const (
	defaultMaxSearchEntries = 10_000
	defaultMaxSearchResults = 1_000
	defaultMaxSearchDepth   = 64
	defaultMaxSearchOutput  = 128 << 10
	defaultMaxSearchFile    = 1 << 20
	maximumGrepLineBytes    = 4096
)

var errStopSearch = errors.New("stop search")

type searchLimitError struct {
	limit string
}

func (e *searchLimitError) Error() string {
	return "search exceeded the configured " + e.limit + " limit"
}

// SearchLimits bound recursive filesystem discovery and model-visible output.
// Zero fields use DefaultSearchLimits.
type SearchLimits struct {
	MaxEntries     int
	MaxResults     int
	MaxDepth       int
	MaxOutputBytes int64
	MaxFileBytes   int64
}

// DefaultSearchLimits returns conservative process-local search limits.
func DefaultSearchLimits() SearchLimits {
	return SearchLimits{
		MaxEntries: defaultMaxSearchEntries, MaxResults: defaultMaxSearchResults, MaxDepth: defaultMaxSearchDepth,
		MaxOutputBytes: defaultMaxSearchOutput, MaxFileBytes: defaultMaxSearchFile,
	}
}

func normalizeSearchLimits(limits SearchLimits) (SearchLimits, error) {
	defaults := DefaultSearchLimits()
	if limits.MaxEntries == 0 {
		limits.MaxEntries = defaults.MaxEntries
	}
	if limits.MaxResults == 0 {
		limits.MaxResults = defaults.MaxResults
	}
	if limits.MaxDepth == 0 {
		limits.MaxDepth = defaults.MaxDepth
	}
	if limits.MaxOutputBytes == 0 {
		limits.MaxOutputBytes = defaults.MaxOutputBytes
	}
	if limits.MaxFileBytes == 0 {
		limits.MaxFileBytes = defaults.MaxFileBytes
	}
	if limits.MaxEntries < 0 || limits.MaxResults < 0 || limits.MaxDepth < 0 || limits.MaxOutputBytes < 0 || limits.MaxFileBytes < 0 {
		return SearchLimits{}, fmt.Errorf("search limits must be positive")
	}
	return limits, nil
}

func normalizedSearchRoot(value string) (string, error) {
	if value == "" {
		return ".", nil
	}
	if len(value) > maximumPathBytes {
		return "", invalidArguments(fmt.Errorf("search path exceeds %d bytes", maximumPathBytes))
	}
	root, err := safespace.NormalizePath(value)
	if err != nil {
		return "", tool.NewError("invalid_path", "The search path is invalid or outside the allowed root.", false, err)
	}
	return root, nil
}

func validatedGlobPattern(pattern string) (string, error) {
	if pattern == "" || len(pattern) > maximumPathBytes {
		return "", invalidArguments(fmt.Errorf("glob pattern must contain between 1 and %d bytes", maximumPathBytes))
	}
	if strings.HasPrefix(pattern, "/") || !doublestar.ValidatePattern(pattern) {
		return "", tool.NewError("invalid_pattern", "The glob pattern is invalid.", false, nil)
	}
	return pattern, nil
}

type walkedEntry struct {
	Path     string
	Relative string
	Size     int64
	IsDir    bool
}

func walkFileSystem(
	ctx context.Context,
	fileSystem safespace.FileSystem,
	root string,
	limits SearchLimits,
	visit func(walkedEntry) error,
) error {
	visited := 0
	var walkDirectory func(string, string, int) error
	walkDirectory = func(directory, relativeDirectory string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := fileSystem.List(ctx, directory)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		sort.Slice(entries, func(first, second int) bool { return entries[first].Name < entries[second].Name })
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !validEntryName(entry.Name) {
				return &tool.RuntimeError{Code: "file_system_failed", Cause: fmt.Errorf("filesystem returned invalid entry name %q", entry.Name)}
			}
			visited++
			if visited > limits.MaxEntries {
				return &searchLimitError{limit: "entry"}
			}
			relative := entry.Name
			if relativeDirectory != "." {
				relative = path.Join(relativeDirectory, entry.Name)
			}
			fullPath := relative
			if root != "." {
				fullPath = path.Join(root, relative)
			}
			value := walkedEntry{Path: fullPath, Relative: relative, Size: entry.Size, IsDir: entry.IsDir}
			if err := visit(value); err != nil {
				return err
			}
			if entry.IsDir {
				if depth >= limits.MaxDepth {
					return &searchLimitError{limit: "depth"}
				}
				if err := walkDirectory(fullPath, relative, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walkDirectory(root, ".", 0)
}

func validEntryName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\\`) && !strings.ContainsRune(name, 0)
}

type boundedResults struct {
	values    []string
	bytes     int64
	truncated bool
	limits    SearchLimits
}

func (r *boundedResults) add(value string) bool {
	addition := int64(len(value))
	if len(r.values) > 0 {
		addition++
	}
	if len(r.values) >= r.limits.MaxResults || r.bytes+addition > r.limits.MaxOutputBytes {
		r.truncated = true
		return false
	}
	r.values = append(r.values, value)
	r.bytes += addition
	return true
}

func truncateUTF8(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	end := maximum - 3
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}
	return value[:end] + "..."
}

func searchFailure(err error) error {
	if err == nil || errors.Is(err, errStopSearch) {
		return nil
	}
	var limitError *searchLimitError
	if errors.As(err, &limitError) {
		return tool.NewError("search_limit_exceeded", "The search exceeded its configured traversal limit.", false, err)
	}
	var runtimeError *tool.RuntimeError
	if errors.As(err, &runtimeError) && runtimeError != nil {
		return runtimeError
	}
	return fileSystemError("list", err)
}
