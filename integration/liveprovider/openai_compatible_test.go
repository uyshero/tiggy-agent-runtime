//go:build liveprovider

package liveprovider

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/uyshero/tiggy-agent-runtime/agent"
	"github.com/uyshero/tiggy-agent-runtime/agenttest"
	"github.com/uyshero/tiggy-agent-runtime/event"
	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	provideropenai "github.com/uyshero/tiggy-agent-runtime/provider/openai"
)

const liveMarker = "TIGGY_RUNTIME_LIVE_OK"

type liveConfig struct {
	provider string
	model    string
	baseURL  string
	apiKey   string
	timeout  time.Duration
}

func TestOpenAICompatibleLiveProvider(t *testing.T) {
	if os.Getenv("TIGGY_RUN_LIVE_PROVIDER_TESTS") != "1" {
		t.Skip("set TIGGY_RUN_LIVE_PROVIDER_TESTS=1 to allow a billed network call")
	}
	config := loadLiveConfig(t)
	limits := agent.DefaultLimits()
	limits.MaxIterations = 1
	limits.MaxModelCalls = 1
	limits.MaxToolCalls = 1
	limits.MaxOutputTokensPerCall = 64
	limits.MaxDuration = config.timeout

	provider, err := provideropenai.New(provideropenai.Config{
		BaseURL: config.baseURL, APIKey: config.apiKey,
		Client:    &http.Client{Timeout: config.timeout},
		AllowHTTP: os.Getenv("TIGGY_LIVE_PROVIDER_ALLOW_HTTP") == "1",
	})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := agent.New(agent.Config{
		Name:         "live-provider-contract",
		SystemPrompt: "This is a protocol conformance check. Follow the requested output exactly.",
		Model:        provider,
		ModelIdentity: model.Identity{
			Provider: config.provider,
			Model:    config.model,
		},
		Limits: limits,
	})
	if err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	result, err := runtime.Run(context.Background(), message.Message{
		Role:       message.RoleUser,
		Visibility: message.VisibilityPublic,
		Content: []message.Content{{
			Type: message.ContentText,
			Text: "Reply with exactly " + liveMarker + " and no other text.",
		}},
	})
	if err != nil {
		t.Fatalf("live Runtime call failed: %v", err)
	}
	if result.Status != agent.StatusCompleted || result.Final == nil {
		t.Fatalf("status/final = %s/%v, want completed/non-nil", result.Status, result.Final != nil)
	}
	if got := strings.TrimSpace(textContent(*result.Final)); got != liveMarker {
		t.Fatalf("provider did not follow the conformance marker; received %d UTF-8 bytes", len(got))
	}
	if result.State.Usage.InputTokens <= 0 || result.State.Usage.OutputTokens <= 0 {
		t.Fatalf("provider Usage is missing input/output tokens: %+v", result.State.Usage)
	}
	if err := agenttest.ValidateTrace(result.Events); err != nil {
		t.Fatalf("Runtime trace: %v", err)
	}
	deltas := countEvents(result.Events, event.ModelCallDelta)
	if deltas == 0 {
		t.Fatal("live Provider produced no model.call.delta Events")
	}
	t.Logf("live provider contract passed: provider=%s model=%s deltas=%d input_tokens=%d output_tokens=%d elapsed=%s",
		config.provider, config.model, deltas, result.State.Usage.InputTokens, result.State.Usage.OutputTokens, time.Since(started).Round(time.Millisecond))
}

func TestReadDotenvTreatsValuesAsData(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), ".env")
	contents := "export TMA_LLM_PROVIDER=custom-provider\n" +
		"TMA_LLM_MODEL=\"quoted-model\"\n" +
		"TMA_LLM_API_KEY='secret # literal'\n" +
		"UNTRUSTED=$(must-not-execute)\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	values, err := readDotenv(path)
	if err != nil {
		t.Fatal(err)
	}
	if values["TMA_LLM_PROVIDER"] != "custom-provider" || values["TMA_LLM_MODEL"] != "quoted-model" ||
		values["TMA_LLM_API_KEY"] != "secret # literal" || values["UNTRUSTED"] != "$(must-not-execute)" {
		t.Fatalf("dotenv values were not parsed literally: %#v", values)
	}
}

func loadLiveConfig(t *testing.T) liveConfig {
	t.Helper()
	values := map[string]string{}
	if path := strings.TrimSpace(os.Getenv("TIGGY_LIVE_PROVIDER_ENV_FILE")); path != "" {
		loaded, err := readDotenv(path)
		if err != nil {
			t.Fatalf("read live Provider environment file: %v", err)
		}
		values = loaded
	}
	lookup := func(key string) string {
		if value, exists := os.LookupEnv(key); exists && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
		return strings.TrimSpace(values[key])
	}
	provider := lookup("TMA_LLM_PROVIDER")
	if provider == "" || provider == "fake" {
		t.Fatal("TMA_LLM_PROVIDER must select a real OpenAI-compatible Provider")
	}
	providerType := lookup("TMA_LLM_PROVIDER_TYPE")
	if providerType != "" && providerType != "openai" && providerType != "openai-compatible" {
		t.Fatalf("unsupported TMA_LLM_PROVIDER_TYPE %q", providerType)
	}
	modelName := lookup("TMA_LLM_MODEL")
	if modelName == "" {
		t.Fatal("TMA_LLM_MODEL is required")
	}
	baseURL := strings.TrimRight(lookup("TMA_LLM_BASE_URL"), "/")
	if baseURL == "" {
		t.Fatal("TMA_LLM_BASE_URL is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		t.Fatal("TMA_LLM_BASE_URL must be an absolute HTTP(S) URL")
	}
	if parsed.Scheme != "https" && os.Getenv("TIGGY_LIVE_PROVIDER_ALLOW_HTTP") != "1" {
		t.Fatal("live Provider requires HTTPS; set TIGGY_LIVE_PROVIDER_ALLOW_HTTP=1 only for a trusted local fixture")
	}
	apiKeyEnv := lookup("TMA_LLM_API_KEY_ENV")
	if apiKeyEnv == "" {
		apiKeyEnv = "TMA_LLM_API_KEY"
	}
	apiKey := lookup(apiKeyEnv)
	if apiKey == "" {
		t.Fatalf("%s is required", apiKeyEnv)
	}
	timeout := 90 * time.Second
	if raw := strings.TrimSpace(os.Getenv("TIGGY_LIVE_PROVIDER_TIMEOUT")); raw != "" {
		parsedTimeout, err := time.ParseDuration(raw)
		if err != nil || parsedTimeout <= 0 {
			t.Fatal("TIGGY_LIVE_PROVIDER_TIMEOUT must be a positive Go duration")
		}
		timeout = parsedTimeout
	}
	return liveConfig{provider: provider, model: modelName, baseURL: baseURL, apiKey: apiKey, timeout: timeout}
}

func readDotenv(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 16<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		separator := strings.IndexByte(line, '=')
		if separator <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:separator])
		if !validEnvKey(key) {
			continue
		}
		value, err := parseDotenvValue(strings.TrimSpace(line[separator+1:]))
		if err != nil {
			return nil, fmt.Errorf("invalid value for %s: %w", key, err)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return values, nil
}

func parseDotenvValue(value string) (string, error) {
	if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		return value[1 : len(value)-1], nil
	}
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		return strconv.Unquote(value)
	}
	if comment := strings.Index(value, " #"); comment >= 0 {
		value = value[:comment]
	}
	return strings.TrimSpace(value), nil
}

func validEnvKey(value string) bool {
	if value == "" {
		return false
	}
	for index, character := range []byte(value) {
		if !(character == '_' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || index > 0 && character >= '0' && character <= '9') {
			return false
		}
	}
	return true
}

func textContent(value message.Message) string {
	var builder strings.Builder
	for _, content := range value.Content {
		if content.Type == message.ContentText {
			builder.WriteString(content.Text)
		}
	}
	return builder.String()
}

func countEvents(values []event.Event, eventType event.Type) int {
	count := 0
	for _, value := range values {
		if value.Type == eventType {
			count++
		}
	}
	return count
}
