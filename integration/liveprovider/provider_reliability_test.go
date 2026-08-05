//go:build liveprovider

package liveprovider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
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

type providerFixture struct {
	name         string
	exchange     roundTripFunc
	configure    func(*agent.Limits)
	wantStatus   agent.Status
	wantClass    agent.ErrorClass
	wantCode     string
	wantProvider *model.ProviderError
}

func TestOpenAICompatibleReliabilityFixtures(t *testing.T) {
	t.Parallel()

	for _, fixture := range providerFixtures() {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			t.Parallel()

			calls := 0
			var requestErr error
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				if err := validateFixtureRequest(request); err != nil {
					requestErr = err
					return nil, err
				}
				return fixture.exchange(request)
			})
			limits := agent.DefaultLimits()
			limits.MaxIterations = 1
			limits.MaxModelCalls = 1
			limits.MaxToolCalls = 1
			limits.MaxOutputTokensPerCall = 32
			limits.MaxDuration = time.Second
			if fixture.configure != nil {
				fixture.configure(&limits)
			}
			provider, err := provideropenai.New(provideropenai.Config{
				BaseURL: "https://provider.invalid/v1", APIKey: "fixture-secret",
				Client: &http.Client{Transport: transport, Timeout: 2 * time.Second},
			})
			if err != nil {
				t.Fatal(err)
			}
			runtime, err := agent.New(agent.Config{
				Name:          "provider-reliability-fixture",
				Model:         provider,
				ModelIdentity: model.Identity{Provider: "fixture", Model: "fixture-model"},
				Limits:        limits, IDs: agenttest.SequentialIDs(),
				Now: agenttest.StepClock(time.Unix(1_700_000_000, 0).UTC(), time.Millisecond),
			})
			if err != nil {
				t.Fatal(err)
			}

			result, runErr := runtime.Run(context.Background(), message.Message{
				Role: message.RoleUser, Visibility: message.VisibilityPublic,
				Content: []message.Content{{Type: message.ContentText, Text: "provider reliability fixture"}},
			})
			if requestErr != nil {
				t.Fatalf("Provider request contract: %v", requestErr)
			}
			if calls != 1 {
				t.Fatalf("HTTP exchanges = %d, want exactly 1", calls)
			}
			if result.Status != fixture.wantStatus {
				t.Fatalf("status = %q, want %q", result.Status, fixture.wantStatus)
			}
			assertFixtureError(t, runErr, fixture)
			if err := result.State.ValidateComplete(); err != nil {
				t.Fatalf("final State: %v", err)
			}
			if err := agenttest.ValidateTrace(result.Events); err != nil {
				t.Fatalf("Runtime trace: %v", err)
			}
			if countEvents(result.Events, event.ModelCallStarted) != 1 || countEvents(result.Events, event.ModelCallCompleted) != 1 {
				t.Fatalf("model lifecycle = %v", agenttest.EventTypes(result.Events))
			}
		})
	}
}

func providerFixtures() []providerFixture {
	return []providerFixture{
		{
			name: "successful stream", exchange: staticExchange(http.StatusOK, validFixtureStream(), nil),
			wantStatus: agent.StatusCompleted,
		},
		{
			name: "authentication rejected", exchange: staticExchange(http.StatusUnauthorized, `{"error":"unauthorized"}`, nil),
			wantStatus: agent.StatusFailed, wantClass: agent.ErrorModel, wantCode: string(model.ErrorAuthentication),
			wantProvider: &model.ProviderError{Class: model.ErrorAuthentication, Code: "http_401"},
		},
		{
			name:       "rate limited without implicit retry",
			exchange:   staticExchange(http.StatusTooManyRequests, `{"error":"rate limited"}`, http.Header{"Retry-After": []string{"2"}}),
			wantStatus: agent.StatusFailed, wantClass: agent.ErrorModel, wantCode: string(model.ErrorRateLimit),
			wantProvider: &model.ProviderError{Class: model.ErrorRateLimit, Code: "http_429", Retryable: true, RetryAfter: 2 * time.Second},
		},
		{
			name: "context length classified", exchange: staticExchange(http.StatusBadRequest,
				`{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"fixture-private"}}`, http.Header{"X-Request-Id": []string{"fixture-context-1"}}),
			wantStatus: agent.StatusFailed, wantClass: agent.ErrorModel, wantCode: string(model.ErrorContextLength),
			wantProvider: &model.ProviderError{Class: model.ErrorContextLength, Code: "context_length_exceeded", RequestID: "fixture-context-1"},
		},
		{
			name: "quota classified as non-retryable", exchange: staticExchange(http.StatusTooManyRequests,
				`{"error":{"type":"insufficient_quota","message":"fixture-private"}}`, nil),
			wantStatus: agent.StatusFailed, wantClass: agent.ErrorModel, wantCode: string(model.ErrorQuota),
			wantProvider: &model.ProviderError{Class: model.ErrorQuota, Code: "insufficient_quota"},
		},
		{
			name: "provider unavailable", exchange: staticExchange(http.StatusServiceUnavailable, `{"error":"unavailable"}`, nil),
			wantStatus: agent.StatusFailed, wantClass: agent.ErrorModel, wantCode: string(model.ErrorUnavailable),
			wantProvider: &model.ProviderError{Class: model.ErrorUnavailable, Code: "http_503", Retryable: true},
		},
		{
			name: "malformed SSE chunk", exchange: staticExchange(http.StatusOK, "data: {not-json}\n\n", http.Header{"Request-Id": []string{"fixture-stream-1"}}),
			wantStatus: agent.StatusFailed, wantClass: agent.ErrorModel, wantCode: string(model.ErrorStreamProtocol),
			wantProvider: &model.ProviderError{Class: model.ErrorStreamProtocol, Code: "invalid_stream_chunk", RequestID: "fixture-stream-1"},
		},
		{
			name: "stream read interrupted", exchange: func(*http.Request) (*http.Response, error) {
				body := io.NopCloser(io.MultiReader(
					strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"),
					errorReader{err: io.ErrUnexpectedEOF},
				))
				return fixtureResponse(http.StatusOK, body, nil), nil
			},
			wantStatus: agent.StatusFailed, wantClass: agent.ErrorModel, wantCode: string(model.ErrorNetwork),
			wantProvider: &model.ProviderError{Class: model.ErrorNetwork, Code: "stream_read_failed", Retryable: true},
		},
		{
			name: "stream ends without finish", exchange: staticExchange(http.StatusOK,
				"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"partial\"}}]}\n\n", nil),
			wantStatus: agent.StatusFailed, wantClass: agent.ErrorModel, wantCode: string(model.ErrorStreamProtocol),
			wantProvider: &model.ProviderError{Class: model.ErrorStreamProtocol, Code: "incomplete_stream"},
		},
		{
			name: "content filtered", exchange: staticExchange(http.StatusOK,
				"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\n", nil),
			wantStatus: agent.StatusFailed, wantClass: agent.ErrorModel, wantCode: string(model.ErrorSafety),
			wantProvider: &model.ProviderError{Class: model.ErrorSafety, Code: "content_filter"},
		},
		{
			name: "request deadline", exchange: func(request *http.Request) (*http.Response, error) {
				<-request.Context().Done()
				return nil, request.Context().Err()
			},
			configure:  func(limits *agent.Limits) { limits.MaxDuration = 50 * time.Millisecond },
			wantStatus: agent.StatusCanceled, wantClass: agent.ErrorCanceled, wantCode: "model_canceled",
		},
		{
			name: "Delta backpressure honored", exchange: staticExchange(http.StatusOK, threeDeltaFixtureStream(), nil),
			configure:  func(limits *agent.Limits) { limits.MaxModelDeltasPerCall = 2 },
			wantStatus: agent.StatusFailed, wantClass: agent.ErrorLimit, wantCode: "max_model_deltas",
		},
	}
}

func assertFixtureError(t *testing.T, runErr error, fixture providerFixture) {
	t.Helper()
	if fixture.wantClass == "" {
		if runErr != nil {
			t.Fatalf("unexpected Run error: %v", runErr)
		}
		return
	}
	var runtimeError *agent.RunError
	if !errors.As(runErr, &runtimeError) || runtimeError.Class != fixture.wantClass || runtimeError.Code != fixture.wantCode {
		t.Fatalf("Run error = %v, want %s/%s", runErr, fixture.wantClass, fixture.wantCode)
	}
	if fixture.wantProvider == nil {
		return
	}
	var providerError *model.ProviderError
	if !errors.As(runErr, &providerError) {
		t.Fatalf("Run error does not preserve ProviderError: %v", runErr)
	}
	want := fixture.wantProvider
	if providerError.Class != want.Class || providerError.Code != want.Code || providerError.Retryable != want.Retryable || providerError.RetryAfter != want.RetryAfter || providerError.RequestID != want.RequestID {
		t.Fatalf("ProviderError = %+v, want class=%s code=%s retryable=%t retry_after=%s request_id=%q",
			providerError, want.Class, want.Code, want.Retryable, want.RetryAfter, want.RequestID)
	}
}

func validateFixtureRequest(request *http.Request) error {
	if request.Method != http.MethodPost || request.URL.String() != "https://provider.invalid/v1/chat/completions" {
		return fmt.Errorf("unexpected endpoint %s %s", request.Method, request.URL)
	}
	if request.Header.Get("Authorization") != "Bearer fixture-secret" || request.Header.Get("Content-Type") != "application/json" {
		return errors.New("required Provider headers are missing")
	}
	encoded, err := io.ReadAll(io.LimitReader(request.Body, 64<<10))
	if err != nil {
		return err
	}
	body := string(encoded)
	for _, required := range []string{`"model":"fixture-model"`, `"stream":true`, `"include_usage":true`, `"content":"provider reliability fixture"`} {
		if !strings.Contains(body, required) {
			return fmt.Errorf("Provider request is missing %s", required)
		}
	}
	return nil
}

func validFixtureStream() string {
	return "data: {\"choices\":[{\"delta\":{\"content\":\"FIXTURE_OK\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":4,\"completion_tokens\":1,\"total_tokens\":5}}\n\n" +
		"data: [DONE]\n\n"
}

func threeDeltaFixtureStream() string {
	return "data: {\"choices\":[{\"delta\":{\"content\":\"one\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"two\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"three\"}}]}\n\n" +
		"data: [DONE]\n\n"
}

func staticExchange(status int, body string, header http.Header) roundTripFunc {
	return func(*http.Request) (*http.Response, error) {
		return fixtureResponse(status, io.NopCloser(strings.NewReader(body)), header), nil
	}
}

func fixtureResponse(status int, body io.ReadCloser, header http.Header) *http.Response {
	if header == nil {
		header = make(http.Header)
	}
	return &http.Response{StatusCode: status, Header: header, Body: body}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}
