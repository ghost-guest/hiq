package deferred

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrSessionInactive means the parent session cannot accept work right now
// (closed, or not the bound session). Delivery is retried, then suppressed —
// a result is never pushed into a session that is gone.
var ErrSessionInactive = errors.New("deferred: session is not active")

// Deliverer is the host half of delivery: how to reach a session, and how to
// surface a result. control.Controller implements it.
type Deliverer interface {
	// DeferredSessionRunnable reports whether the session can accept work now.
	DeferredSessionRunnable(sessionPath string) bool
	// DeliverDeferred hands the text to the session. triggerTurn asks for a new
	// turn to start from it; otherwise it must only become part of the
	// session's context for the next turn, plus a user-visible notice.
	DeliverDeferred(sessionPath, text string, triggerTurn bool) error
	// NotifyDeferred surfaces an immediate, best-effort user notice. Failures
	// here never block or fail delivery.
	NotifyDeferred(sessionPath, title, body string) error
}

// Options configures a Coordinator. Zero values pick the defaults described on
// each field.
type Options struct {
	Policy Policy
	// RetryInterval is how often undelivered tasks are retried. 0 → 30s.
	RetryInterval time.Duration
	// MaxAttempts caps retries before a task is suppressed. 0 → 20.
	MaxAttempts int
	// BodyLimit truncates a stored result body (bytes). 0 → 4000.
	BodyLimit int
	// RetainDelivered is how long delivered rows are kept for auditing. 0 → 7d.
	RetainDelivered time.Duration
	Logf            func(string, ...any)
	Now             func() time.Time
}

// Defaults for Options.
const (
	DefaultRetryInterval  = 30 * time.Second
	DefaultMaxAttempts    = 20
	DefaultBodyLimit      = 4000
	DefaultRetainDelivered = 7 * 24 * time.Hour
)

// Coordinator owns the durable deferred-task tables and drives delivery. It is
// safe for concurrent use.
type Coordinator struct {
	opts Options

	mu        sync.Mutex
	stores    map[string]*Store
	inflight  map[string]struct{}
	deliverer Deliverer
	started   bool
	stopCh    chan struct{}
	wg        sync.WaitGroup
}

// NewCoordinator builds a coordinator. Call SetDeliverer and Start to activate.
func NewCoordinator(opts Options) *Coordinator {
	if opts.RetryInterval <= 0 {
		opts.RetryInterval = DefaultRetryInterval
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = DefaultMaxAttempts
	}
	if opts.BodyLimit <= 0 {
		opts.BodyLimit = DefaultBodyLimit
	}
	if opts.RetainDelivered <= 0 {
		opts.RetainDelivered = DefaultRetainDelivered
	}
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Coordinator{
		opts:     opts,
		stores:   map[string]*Store{},
		inflight: map[string]struct{}{},
	}
}

// SetDeliverer installs the host's delivery implementation. Until it is set,
// tasks are stored but not delivered.
func (c *Coordinator) SetDeliverer(d Deliverer) {
	c.mu.Lock()
	c.deliverer = d
	c.mu.Unlock()
}

// Policy returns the session-level policy in force.
func (c *Coordinator) Policy() Policy { return c.opts.Policy }

// Start launches the retry ticker. Idempotent.
func (c *Coordinator) Start() {
	c.mu.Lock()
	if c.started {
		c.mu.Unlock()
		return
	}
	c.started = true
	c.stopCh = make(chan struct{})
	stop := c.stopCh
	c.mu.Unlock()

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		t := time.NewTicker(c.opts.RetryInterval)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				c.retryAll()
			}
		}
	}()
}

// Close stops the ticker and waits for it to return.
func (c *Coordinator) Close() {
	c.mu.Lock()
	stop := c.stopCh
	c.started = false
	c.stopCh = nil
	c.mu.Unlock()
	if stop != nil {
		close(stop)
	}
	c.wg.Wait()
}

// store returns the session's task table, opening it on first use.
func (c *Coordinator) store(sessionPath string) (*Store, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.stores[sessionPath]; ok {
		return s, nil
	}
	s, err := OpenStore(sessionPath, c.opts.Logf)
	if err != nil {
		return nil, err
	}
	// One clock for the whole coordinator: task timestamps, retry bookkeeping
	// and pruning must agree.
	s.WithNow(c.opts.Now)
	c.stores[sessionPath] = s
	return s, nil
}

// Store exposes a session's table (nil when the session path is unusable), for
// frontends that want to list or clear results.
func (c *Coordinator) Store(sessionPath string) (*Store, bool) {
	s, err := c.store(sessionPath)
	if err != nil {
		return nil, false
	}
	return s, true
}

// Enqueue records a task and, when it is already terminal, kicks off delivery
// on its own goroutine so the producing code (a job's goroutine) never blocks
// on file I/O or a session hand-off.
func (c *Coordinator) Enqueue(sessionPath string, t Task) error {
	if strings.TrimSpace(sessionPath) == "" {
		return errors.New("deferred: session path is required")
	}
	if t.ID == "" {
		return errors.New("deferred: task id is required")
	}
	if strings.TrimSpace(t.SessionPath) == "" {
		t.SessionPath = sessionPath
	}
	t.Body = truncateBody(t.Body, c.opts.BodyLimit)

	s, err := c.store(sessionPath)
	if err != nil {
		return err
	}
	if err := s.Put(t); err != nil {
		return err
	}
	if !t.Deliverable() {
		return nil
	}
	go func() {
		if err := c.DeliverNow(sessionPath, t.ID); err != nil && !errors.Is(err, ErrSessionInactive) {
			c.opts.Logf("deferred: deliver %s: %v", t.ID, err)
		}
	}()
	return nil
}

// DeliverNow attempts one delivery synchronously. Nil means delivered (or
// nothing left to do); ErrSessionInactive means retry later.
func (c *Coordinator) DeliverNow(sessionPath, id string) error {
	key := sessionPath + "\x00" + id
	c.mu.Lock()
	if _, busy := c.inflight[key]; busy {
		c.mu.Unlock()
		return nil // another delivery for this task is already running
	}
	c.inflight[key] = struct{}{}
	d := c.deliverer
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.inflight, key)
		c.mu.Unlock()
	}()

	s, err := c.store(sessionPath)
	if err != nil {
		return err
	}
	t, ok := s.Query(id)
	if !ok {
		return fmt.Errorf("deferred: unknown task %q", id)
	}
	if !t.Deliverable() {
		return nil
	}
	if d == nil {
		return errors.New("deferred: no deliverer installed")
	}
	if !d.DeferredSessionRunnable(sessionPath) {
		if n, err := s.BumpAttempts(id); err == nil && n >= c.opts.MaxAttempts {
			// Give up rather than retry forever against a session that is gone.
			_ = s.Suppress(id, "parent session is no longer active")
		}
		return ErrSessionInactive
	}

	// The user-facing notice is best-effort and independent of the durable
	// hand-off: a notice failure must not lose the result.
	if c.opts.Policy.Notify(t) {
		body := truncateBody(t.Body, noticeBodyLimit)
		if nerr := d.NotifyDeferred(sessionPath, t.Title, body); nerr != nil {
			c.opts.Logf("deferred: notify %s: %v", t.ID, nerr)
		}
	}
	trigger := c.opts.Policy.TriggerTurn(t)
	if err := d.DeliverDeferred(sessionPath, t.Wrapped(), trigger); err != nil {
		if n, berr := s.BumpAttempts(id); berr == nil && n >= c.opts.MaxAttempts {
			_ = s.Suppress(id, fmt.Sprintf("delivery failed %d times: %v", n, err))
		}
		return fmt.Errorf("deferred: deliver %s: %w", id, err)
	}
	return s.MarkDelivered(id)
}

// noticeBodyLimit bounds how much output rides the immediate notice (the full
// body still reaches the model through the session context).
const noticeBodyLimit = 400

// FlushSession delivers everything still pending for one session. Called when a
// session is (re)bound and on startup, so results produced before a crash or
// while the session was closed are not lost.
func (c *Coordinator) FlushSession(sessionPath string) {
	if strings.TrimSpace(sessionPath) == "" {
		return
	}
	s, err := c.store(sessionPath)
	if err != nil {
		c.opts.Logf("deferred: open %s: %v", sessionPath, err)
		return
	}
	for _, t := range s.ListUndelivered() {
		if err := c.DeliverNow(sessionPath, t.ID); err != nil && !errors.Is(err, ErrSessionInactive) {
			c.opts.Logf("deferred: flush %s: %v", t.ID, err)
		}
	}
	if n, err := s.PruneDelivered(c.opts.RetainDelivered); err != nil {
		c.opts.Logf("deferred: prune %s: %v", sessionPath, err)
	} else if n > 0 {
		c.opts.Logf("deferred: pruned %d delivered task(s) for %s", n, sessionPath)
	}
}

// retryAll is the ticker body: retry every known session's pending results.
func (c *Coordinator) retryAll() {
	c.mu.Lock()
	paths := make([]string, 0, len(c.stores))
	for p := range c.stores {
		paths = append(paths, p)
	}
	c.mu.Unlock()
	for _, p := range paths {
		c.FlushSession(p)
	}
}

// truncateBody keeps the head and tail of an oversized result (the head says
// what happened, the tail is where errors surface) with a marker between them.
func truncateBody(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	head := limit * 2 / 3
	tail := limit - head
	return s[:head] + fmt.Sprintf("\n... (truncated %d bytes) ...\n", len(s)-limit) + s[len(s)-tail:]
}
