// Package contextmanager 在生成前规划有界模型上下文。
package contextmanager

import (
	"context"
	"fmt"
	"runtime/debug"

	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
)

const (
	defaultWarningBasisPoints  int64 = 8_000
	defaultCriticalBasisPoints int64 = 9_500
	maxTokenValue                    = int64(1 << 40)
)

// CountSource 标明 Token 计数来自供应商还是估算。
type CountSource string

const (
	CountSourceProvider  CountSource = "provider"
	CountSourceEstimated CountSource = "estimated"
)

// Pressure 描述相对上下文窗口的压力等级。
type Pressure string

const (
	PressureNormal   Pressure = "normal"
	PressureWarning  Pressure = "warning"
	PressureCritical Pressure = "critical"
	PressureOverflow Pressure = "overflow"
)

// Request 是一次上下文规划输入。
type Request struct {
	Model               model.Identity
	Messages            []message.Message
	Tools               []model.ToolDefinition
	OutputReserveTokens int64
}

func (r Request) Validate() error {
	if err := r.Model.Validate(); err != nil {
		return err
	}
	if len(r.Messages) == 0 {
		return fmt.Errorf("context messages are required")
	}
	for index, value := range r.Messages {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("message %d: %w", index, err)
		}
	}
	for index, value := range r.Tools {
		if err := value.Validate(); err != nil {
			return fmt.Errorf("tool %d: %w", index, err)
		}
	}
	if r.OutputReserveTokens <= 0 || r.OutputReserveTokens > maxTokenValue {
		return fmt.Errorf("output reserve must be between 1 and %d tokens", maxTokenValue)
	}
	return nil
}

type Plan struct {
	Model                        model.Identity `json:"model"`
	CountSource                  CountSource    `json:"count_source"`
	ContextWindowTokens          int64          `json:"context_window_tokens"`
	InputTokens                  int64          `json:"input_tokens"`
	OutputReserveTokens          int64          `json:"output_reserve_tokens"`
	SafetyMarginTokens           int64          `json:"safety_margin_tokens"`
	RequiredTokens               int64          `json:"required_tokens"`
	AvailableInputTokens         int64          `json:"available_input_tokens"`
	RemainingInputTokens         int64          `json:"remaining_input_tokens"`
	OverflowTokens               int64          `json:"overflow_tokens"`
	UtilizationBasisPoints       int64          `json:"utilization_basis_points"`
	WarningThresholdBasisPoints  int64          `json:"warning_threshold_basis_points"`
	CriticalThresholdBasisPoints int64          `json:"critical_threshold_basis_points"`
	Pressure                     Pressure       `json:"pressure"`
	Fits                         bool           `json:"fits"`
}

func (p Plan) Validate() error {
	if err := p.Model.Validate(); err != nil {
		return err
	}
	if p.CountSource != CountSourceProvider && p.CountSource != CountSourceEstimated {
		return fmt.Errorf("unsupported token count source %q", p.CountSource)
	}
	if p.ContextWindowTokens <= 0 || p.ContextWindowTokens > maxTokenValue || p.InputTokens <= 0 || p.InputTokens > maxTokenValue {
		return fmt.Errorf("context window and input count must be within supported bounds")
	}
	if p.OutputReserveTokens <= 0 || p.OutputReserveTokens > maxTokenValue || p.SafetyMarginTokens < 0 || p.SafetyMarginTokens > maxTokenValue {
		return fmt.Errorf("output reserve must be positive and safety margin cannot be negative")
	}
	if p.WarningThresholdBasisPoints <= 0 || p.WarningThresholdBasisPoints >= p.CriticalThresholdBasisPoints || p.CriticalThresholdBasisPoints >= 10_000 {
		return fmt.Errorf("pressure thresholds must satisfy 0 < warning < critical < 10000")
	}
	available := p.ContextWindowTokens - p.OutputReserveTokens - p.SafetyMarginTokens
	if available <= 0 || p.AvailableInputTokens != available {
		return fmt.Errorf("available input budget is inconsistent")
	}
	required := p.InputTokens + p.OutputReserveTokens + p.SafetyMarginTokens
	if required <= 0 || p.RequiredTokens != required {
		return fmt.Errorf("required token budget is inconsistent")
	}
	remaining := max64(available-p.InputTokens, 0)
	overflow := max64(p.InputTokens-available, 0)
	if p.RemainingInputTokens != remaining || p.OverflowTokens != overflow || p.Fits != (overflow == 0) {
		return fmt.Errorf("remaining or overflow budget is inconsistent")
	}
	utilization := utilizationBasisPoints(p.InputTokens, available)
	if p.UtilizationBasisPoints != utilization {
		return fmt.Errorf("context utilization is inconsistent")
	}
	expectedPressure := classifyPressure(utilization, overflow, p.WarningThresholdBasisPoints, p.CriticalThresholdBasisPoints)
	if p.Pressure != expectedPressure {
		return fmt.Errorf("context pressure is inconsistent")
	}
	return nil
}

type Planner interface {
	Plan(context.Context, Request) (Plan, error)
}

type TokenPlannerOptions struct {
	Counter                      model.TokenCounter
	CountSource                  CountSource
	ContextWindowTokens          int64
	SafetyMarginTokens           int64
	WarningThresholdBasisPoints  int64
	CriticalThresholdBasisPoints int64
}

// TokenPlanner is immutable. It is concurrency-safe when its Counter is.
type TokenPlanner struct {
	counter  model.TokenCounter
	source   CountSource
	window   int64
	margin   int64
	warning  int64
	critical int64
}

func NewTokenPlanner(options TokenPlannerOptions) (*TokenPlanner, error) {
	if options.Counter == nil {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: fmt.Errorf("token counter is required")}
	}
	if options.CountSource != CountSourceProvider && options.CountSource != CountSourceEstimated {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: fmt.Errorf("token count source must be provider or estimated")}
	}
	if options.ContextWindowTokens <= 0 || options.ContextWindowTokens > maxTokenValue {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: fmt.Errorf("context window must be between 1 and %d tokens", maxTokenValue)}
	}
	if options.SafetyMarginTokens < 0 || options.SafetyMarginTokens >= options.ContextWindowTokens {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: fmt.Errorf("safety margin must be non-negative and smaller than the context window")}
	}
	warning := options.WarningThresholdBasisPoints
	if warning == 0 {
		warning = defaultWarningBasisPoints
	}
	critical := options.CriticalThresholdBasisPoints
	if critical == 0 {
		critical = defaultCriticalBasisPoints
	}
	if warning <= 0 || warning >= critical || critical >= 10_000 {
		return nil, &Error{Code: ErrorInvalidConfig, Cause: fmt.Errorf("pressure thresholds must satisfy 0 < warning < critical < 10000")}
	}
	return &TokenPlanner{
		counter: options.Counter, source: options.CountSource, window: options.ContextWindowTokens,
		margin: options.SafetyMarginTokens, warning: warning, critical: critical,
	}, nil
}

func (p *TokenPlanner) Plan(ctx context.Context, request Request) (Plan, error) {
	if ctx == nil || p == nil {
		return Plan{}, &Error{Code: ErrorInvalidRequest, Cause: fmt.Errorf("context and planner are required")}
	}
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	if err := request.Validate(); err != nil {
		return Plan{}, &Error{Code: ErrorInvalidRequest, Cause: err}
	}
	available := p.window - request.OutputReserveTokens - p.margin
	if available <= 0 {
		return Plan{}, &Error{Code: ErrorImpossibleBudget, Cause: fmt.Errorf("output reserve and safety margin consume the context window")}
	}
	countRequest := model.TokenRequest{
		Model: request.Model, Messages: message.CloneAll(request.Messages), Tools: cloneToolDefinitions(request.Tools),
	}
	inputTokens, err := countTokens(ctx, p.counter, countRequest)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Plan{}, ctxErr
		}
		return Plan{}, &Error{Code: ErrorCounterFailed, Cause: err}
	}
	if inputTokens <= 0 || inputTokens > maxTokenValue {
		return Plan{}, &Error{Code: ErrorInvalidCount, Cause: fmt.Errorf("token counter returned %d", inputTokens)}
	}
	utilization := utilizationBasisPoints(inputTokens, available)
	overflow := max64(inputTokens-available, 0)
	plan := Plan{
		Model: request.Model, CountSource: p.source, ContextWindowTokens: p.window,
		InputTokens: inputTokens, OutputReserveTokens: request.OutputReserveTokens, SafetyMarginTokens: p.margin,
		RequiredTokens: inputTokens + request.OutputReserveTokens + p.margin, AvailableInputTokens: available,
		RemainingInputTokens: max64(available-inputTokens, 0), OverflowTokens: overflow,
		UtilizationBasisPoints: utilization, WarningThresholdBasisPoints: p.warning, CriticalThresholdBasisPoints: p.critical,
		Pressure: classifyPressure(utilization, overflow, p.warning, p.critical), Fits: overflow == 0,
	}
	if err := plan.Validate(); err != nil {
		return Plan{}, &Error{Code: ErrorInvalidPlan, Cause: err}
	}
	return plan, nil
}

func countTokens(ctx context.Context, counter model.TokenCounter, request model.TokenRequest) (tokens int64, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("token counter panic: %v\n%s", recovered, debug.Stack())
		}
	}()
	return counter.CountTokens(ctx, request)
}

func cloneToolDefinitions(values []model.ToolDefinition) []model.ToolDefinition {
	result := make([]model.ToolDefinition, len(values))
	for index, value := range values {
		result[index] = value.Clone()
	}
	return result
}

func utilizationBasisPoints(input, available int64) int64 {
	return input * 10_000 / available
}

func classifyPressure(utilization, overflow, warning, critical int64) Pressure {
	if overflow > 0 {
		return PressureOverflow
	}
	if utilization >= critical {
		return PressureCritical
	}
	if utilization >= warning {
		return PressureWarning
	}
	return PressureNormal
}

func max64(first, second int64) int64 {
	if first > second {
		return first
	}
	return second
}
