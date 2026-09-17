package cli

import (
	"context"
	"time"

	"github.com/zzycxz/fairpeer/internal/config"
	"github.com/zzycxz/fairpeer/internal/stats"
)

// closeCLIUsageCatalogs drains the accepted usage records and fences the
// process-wide projection worker before a CLI invocation returns.
//
// Usage recording is asynchronous so a chat turn never waits on disk, which
// means the last rows of a run are still queued when the command finishes — and
// the SQLite projection keeps an open handle on the user's cache directory.
// Without this, a short `fairpeer run` loses its final usage and (on Windows,
// where an open handle blocks deletion) any test that redirects the state home
// fails its TempDir cleanup.
func closeCLIUsageCatalogs() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = stats.Flush(ctx, config.StatsDir())
	_ = stats.CloseUsageCatalogs(ctx)
}
