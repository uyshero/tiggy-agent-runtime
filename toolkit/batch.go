package toolkit

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/tool"
)

const defaultBatchConcurrency = 4
const maximumBatchConcurrency = 64

type BatchOptions struct {
	MaxConcurrency int
}

type BatchResult struct {
	CallID string
	Name   string
	Result tool.Result
	Err    error
}

// CallBatch executes calls in order-separated phases. A sequential Tool is a
// barrier and never overlaps another call. Consecutive Tools explicitly marked
// safe may overlap up to MaxConcurrency. Results preserve input order.
func (v View) CallBatch(ctx context.Context, calls []message.ToolCall, options BatchOptions) []BatchResult {
	calls = cloneToolCalls(calls)
	results := make([]BatchResult, len(calls))
	maximum := options.MaxConcurrency
	if maximum <= 0 {
		maximum = defaultBatchConcurrency
	}
	if maximum > maximumBatchConcurrency {
		maximum = maximumBatchConcurrency
	}
	for index := 0; index < len(calls); {
		if v.concurrencyClass(calls[index].Name) != model.ToolConcurrencySafe {
			results[index] = v.callForBatch(ctx, calls[index])
			index++
			continue
		}
		end := index + 1
		for end < len(calls) && v.concurrencyClass(calls[end].Name) == model.ToolConcurrencySafe {
			end++
		}
		v.callConcurrent(ctx, calls, results, index, end, maximum)
		index = end
	}
	return results
}

func cloneToolCalls(values []message.ToolCall) []message.ToolCall {
	cloned := make([]message.ToolCall, len(values))
	for index, value := range values {
		cloned[index] = value
		cloned[index].Arguments = append(json.RawMessage(nil), value.Arguments...)
	}
	return cloned
}

func (v View) callConcurrent(
	ctx context.Context,
	calls []message.ToolCall,
	results []BatchResult,
	start int,
	end int,
	maximum int,
) {
	workers := maximum
	if count := end - start; workers > count {
		workers = count
	}
	jobs := make(chan int)
	var wait sync.WaitGroup
	wait.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func() {
			defer wait.Done()
			for index := range jobs {
				results[index] = v.callForBatch(ctx, calls[index])
			}
		}()
	}
	for index := start; index < end; index++ {
		jobs <- index
	}
	close(jobs)
	wait.Wait()
}

func (v View) callForBatch(ctx context.Context, call message.ToolCall) BatchResult {
	cloned := call
	cloned.Arguments = append(json.RawMessage(nil), call.Arguments...)
	result, err := v.Call(ctx, cloned)
	return BatchResult{CallID: call.ID, Name: call.Name, Result: result, Err: err}
}

func (v View) concurrencyClass(name string) model.ToolConcurrencyClass {
	entry, exists := v.tools[name]
	if !exists || entry.definition.ConcurrencyClass == "" {
		return model.ToolConcurrencySequential
	}
	return entry.definition.ConcurrencyClass
}
