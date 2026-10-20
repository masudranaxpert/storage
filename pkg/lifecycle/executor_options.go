package lifecycle

import (
	"strings"
	"time"

	"stream/pkg/storage"
)

type ExecutorOption func(*Executor)

// WithExecutorSecret sets the shared cluster secret used to authenticate agent calls.
func WithExecutorSecret(secret string) ExecutorOption {
	return func(ex *Executor) {
		ex.secret = secret
	}
}

// WithCoordinatorURL sets the base URL agents use to post task progress back to the coordinator.
func WithCoordinatorURL(baseURL string) ExecutorOption {
	return func(ex *Executor) {
		ex.coordinatorURL = strings.TrimRight(baseURL, "/")
	}
}

// WithExecutorBucketResolver injects remote S3/R2 bucket config resolution.
func WithExecutorBucketResolver(fn func(id string) (*storage.S3Config, error)) ExecutorOption {
	return func(ex *Executor) {
		ex.bucketResolver = fn
	}
}

// WithExecutorUsageFunc injects live per-node/per-block media byte usage for destination block selection.
func WithExecutorUsageFunc(fn func() map[string]uint64) ExecutorOption {
	return func(ex *Executor) {
		if fn != nil {
			ex.usage = fn
		}
	}
}

// WithExecutorBackoffs overrides the retry backoff schedule (default 1m, 5m, 15m).
func WithExecutorBackoffs(backoffs []time.Duration) ExecutorOption {
	return func(ex *Executor) {
		if len(backoffs) > 0 {
			ex.backoffs = append([]time.Duration(nil), backoffs...)
		}
	}
}

// WithExecutorConcurrency overrides the global (default 4) and per-node (default 2) task limits.
func WithExecutorConcurrency(globalLimit, perNodeLimit int) ExecutorOption {
	return func(ex *Executor) {
		if globalLimit > 0 {
			ex.globalLimit = globalLimit
		}
		if perNodeLimit > 0 {
			ex.perNodeLimit = perNodeLimit
		}
	}
}

// WithExecutorTimeout overrides the per-task timeout (default 6h).
func WithExecutorTimeout(d time.Duration) ExecutorOption {
	return func(ex *Executor) {
		if d > 0 {
			ex.taskTimeout = d
		}
	}
}

// WithExecutorPollInterval overrides the scheduler/copy poll interval.
func WithExecutorPollInterval(d time.Duration) ExecutorOption {
	return func(ex *Executor) {
		if d > 0 {
			ex.pollInterval = d
		}
	}
}

// WithExecutorClock injects a custom clock for deterministic tests.
func WithExecutorClock(fn func() time.Time) ExecutorOption {
	return func(ex *Executor) {
		if fn != nil {
			ex.now = fn
		}
	}
}

// NewExecutor creates a new lifecycle task executor with spec §5.3 defaults.
