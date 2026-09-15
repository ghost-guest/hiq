package provider

import (
	"context"
	"sync/atomic"
)

// requestAttemptCounterKey carries a per-stream HTTP request counter through the
// call context. WithRequestAttemptCounter installs one; SendWithRetry bumps it
// via recordRequestAttempt, and providers attach the final count to the stream's
// Usage record so callers can account for API calls that failed before producing
// token usage.
type requestAttemptCounterKey struct{}

type requestAttemptCounter struct {
	count atomic.Int64
}

// WithRequestAttemptCounter returns a context that counts every HTTP request
// SendWithRetry starts. An existing counter is reused so a caller can observe
// attempts even when the provider returns before producing a Usage chunk.
// Provider implementations use one counter for a logical stream (including
// header retries and safe reconnects), then attach the final count to the
// stream's Usage record.
func WithRequestAttemptCounter(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if counter, _ := ctx.Value(requestAttemptCounterKey{}).(*requestAttemptCounter); counter != nil {
		return ctx
	}
	return context.WithValue(ctx, requestAttemptCounterKey{}, &requestAttemptCounter{})
}

// WithIndependentRequestAttemptCounter gives an auxiliary call its own usage
// count while preserving cancellation and other context values from its parent.
func WithIndependentRequestAttemptCounter(ctx context.Context) context.Context {
	return context.WithValue(ctx, requestAttemptCounterKey{}, &requestAttemptCounter{})
}

// RequestAttemptCount returns the number of HTTP requests started through
// SendWithRetry for the counter attached to ctx.
func RequestAttemptCount(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	counter, _ := ctx.Value(requestAttemptCounterKey{}).(*requestAttemptCounter)
	if counter == nil {
		return 0
	}
	return int(counter.count.Load())
}

// ApplyRequestAttemptCount copies the stream's exact HTTP request count into a
// Usage record. Contexts without a counter leave the record unchanged so custom
// providers keep the zero-means-one compatibility contract.
func ApplyRequestAttemptCount(ctx context.Context, usage *Usage) {
	if usage == nil {
		return
	}
	if count := RequestAttemptCount(ctx); count > 0 {
		usage.RequestCount = count
	}
}

// UsageWithRequestAttemptCount returns a copy of usage carrying the exact
// number of HTTP requests observed through ctx. When a provider request fails
// before producing token usage, it returns a request-only Usage record so
// callers can still account for the API calls. If neither usage nor attempts
// exist, it returns nil.
func UsageWithRequestAttemptCount(ctx context.Context, usage *Usage) *Usage {
	count := RequestAttemptCount(ctx)
	if usage == nil {
		if count <= 0 {
			return nil
		}
		return &Usage{RequestCount: count, Unknown: true}
	}
	result := *usage
	if count > 0 {
		result.RequestCount = count
	}
	return &result
}

// recordRequestAttempt bumps the counter installed by WithRequestAttemptCounter.
// It is called once per HTTP request SendWithRetry issues, including retries.
func recordRequestAttempt(ctx context.Context) {
	if ctx == nil {
		return
	}
	counter, _ := ctx.Value(requestAttemptCounterKey{}).(*requestAttemptCounter)
	if counter != nil {
		counter.count.Add(1)
	}
}
