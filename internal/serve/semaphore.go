package serve

import (
	"context"
	"encoding/json"
)

// semaphoreWrap gates a queue exec behind a shared semaphore (SPEC-06:
// ONE root semaphore bounds concurrent compiles ACROSS all per-workspace
// stacks; per-stack queues keep their own FIFO and recovery semantics).
// A nil sem returns exec unwrapped (single-workspace path unchanged).
func semaphoreWrap(sem chan struct{}, exec func(context.Context, *Job) (json.RawMessage, error)) func(context.Context, *Job) (json.RawMessage, error) {
	if sem == nil {
		return exec
	}
	return func(ctx context.Context, j *Job) (json.RawMessage, error) {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
			return exec(ctx, j)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
