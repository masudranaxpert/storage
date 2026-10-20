package lifecycle

import (
	"sync"
	"time"

	"stream/internal/cluster"
	"stream/internal/model"
	"stream/internal/storage"
)

type Option func(*Engine)

// WithUsageFunc injects a callback returning live per-node/per-block media byte usage.
func WithUsageFunc(fn func() map[string]uint64) Option {
	return func(e *Engine) {
		if fn != nil {
			e.usage = fn
		}
	}
}

// WithClock injects a custom clock for deterministic testing.
func WithClock(fn func() time.Time) Option {
	return func(e *Engine) {
		if fn != nil {
			e.now = fn
		}
	}
}

// WithJobStore injects the JobStore used by the relocation executor.
func WithJobStore(js model.JobStore) Option {
	return func(e *Engine) {
		e.jobStore = js
	}
}

// WithSecret injects the shared cluster secret used to authenticate outgoing agent requests.
func WithSecret(secret string) Option {
	return func(e *Engine) {
		e.secret = secret
	}
}

// WithExecutorOptions appends options forwarded to the relocation Executor.
func WithExecutorOptions(opts ...ExecutorOption) Option {
	return func(e *Engine) {
		e.executorOpts = append(e.executorOpts, opts...)
	}
}

// WithBucketResolver injects bucket configuration lookup for S3/R2 remote lifecycle relocations.
func WithBucketResolver(fn func(id string) (*storage.S3Config, error)) Option {
	return func(e *Engine) {
		e.executorOpts = append(e.executorOpts, WithExecutorBucketResolver(fn))
	}
}

// Engine manages the tier flow policy, placement simulation, hit recording, evaluation, and task execution.
type Engine struct {
	mu           sync.RWMutex
	store        Store
	jobStore     model.JobStore
	tiers        *storage.Manager
	coord        *cluster.Coordinator
	usage        func() map[string]uint64
	now          func() time.Time
	secret       string
	executorOpts []ExecutorOption
	executor     *Executor
	hits         *HitRecorder
	viewers      *ActiveViewerTracker
	kickCh       chan struct{}
	policy       FlowPolicy
}

// New constructs a lifecycle Engine and persists DefaultPolicy on first boot if none exists.
