package main

import (
	"context"

	"github.com/codex/zjmfgo/internal/correlation"
	"github.com/codex/zjmfgo/internal/scheduler"
	"github.com/codex/zjmfgo/internal/store"
)

func runWorkerPass(ctx context.Context, lane *store.WorkerRuntimeLane, run func(context.Context)) {
	if ctx.Err() != nil {
		return
	}
	lane.Observe(true)
	defer lane.Observe(false)
	ctx = lane.Context(correlation.WithID(ctx, correlation.NewID()))
	run(ctx)
}

func runObservedScheduledLane(ctx context.Context, task scheduler.Descriptor, lane *store.WorkerRuntimeLane, run func(context.Context)) {
	runScheduledLane(ctx, task, func() { runWorkerPass(ctx, lane, run) })
}

// Each queued job restores its durable origin. Old rows without an origin get
// a fresh attempt ID; they never inherit a previous job or scheduler pass ID.
func workerJobContext(ctx context.Context, id string) context.Context {
	return correlation.JobContext(ctx, id)
}
