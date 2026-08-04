package toolkit

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/uyshero/tiggy-agent-runtime/message"
	"github.com/uyshero/tiggy-agent-runtime/model"
	"github.com/uyshero/tiggy-agent-runtime/tool"
)

const defaultBatchConcurrency = 4  // 默认并发 worker 数
const maximumBatchConcurrency = 64 // 硬上限

// BatchOptions 控制 CallBatch 的并发度；MaxConcurrency<=0 时用默认值。
type BatchOptions struct {
	MaxConcurrency int
}

// BatchResult 对应一次 Tool Call 的有序结果。
type BatchResult struct {
	CallID string
	Name   string
	Result tool.Result
	Err    error
}

// CallBatch 按「顺序屏障」分阶段执行：sequential 工具独占且不与其它调用重叠；
// 连续声明为 safe 的工具可在 MaxConcurrency 内并行。结果始终按输入顺序返回。
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
