package projectkb

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// 变更即同步 (sync-on-change).
//
// A long dev session edits code and docs constantly. Making the user remember to
// press 同步 (or hoping they call kb_sync) means the project map silently drifts
// out of date exactly when it is most useful. The watcher closes that gap: it
// notices the tree moving and re-syncs on its own, debounced.
//
// Polling (rather than fsnotify) is a deliberate trade:
//
//   - it adds no dependency, which matters for a portable single-binary app;
//   - it works identically on Windows, network shares and WSL mounts, where
//     native recursive watchers are unreliable or capped;
//   - it is cheap. The poll only stats files (no reads) and hashes
//     path+size+mtime; the real content diff still happens in Sync, so this
//     layer only has to answer "did anything plausible move?".
//
// Debounce matters more than it sounds: an editor saving five files, or a build
// rewriting dist/, would otherwise fire five syncs. Changes must go quiet for
// Debounce before one sync runs.

// WatchOptions tunes a Watcher.
type WatchOptions struct {
	// Interval is the poll interval (default 3s).
	Interval time.Duration
	// Debounce is the quiet period a change must hold before the sync fires
	// (default 2s).
	Debounce time.Duration
	// Roots are the directories to watch. Empty = the hub's working directory.
	Roots []string
	// MaxFiles bounds one fingerprint scan (default 6000).
	MaxFiles int
}

// Watcher is a polling change watcher for one Hub.
type Watcher struct {
	hub      *Hub
	opts     WatchOptions
	onChange func(Report)

	mu         sync.Mutex
	running    bool
	stop       chan struct{}
	done       chan struct{}
	last       string
	dirtySince time.Time
	// syncs counts completed auto-syncs (panel liveness: "已自动同步 N 次").
	syncs   int
	lastRep Report
}

// Watch creates a watcher for the hub. It does not start polling until Start is
// called, so the caller can decide the lifetime (typically: while the panel is
// open, or while the app is running with the feature enabled).
func (h *Hub) Watch(opts WatchOptions, onChange func(Report)) *Watcher {
	if opts.Interval <= 0 {
		opts.Interval = 3 * time.Second
	}
	if opts.Debounce <= 0 {
		opts.Debounce = 2 * time.Second
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = 6000
	}
	if len(opts.Roots) == 0 {
		opts.Roots = []string{h.cwd}
	}
	return &Watcher{
		hub:      h,
		opts:     opts,
		onChange: onChange,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start begins polling. Calling it on a running watcher is a no-op.
func (w *Watcher) Start() {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return
	}
	w.running = true
	w.last = scanFingerprint(w.opts.Roots, w.opts.MaxFiles)
	w.mu.Unlock()
	go w.loop()
}

// Stop halts polling and waits for the loop to exit. Safe to call twice.
func (w *Watcher) Stop() {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return
	}
	w.running = false
	close(w.stop)
	w.mu.Unlock()
	<-w.done
}

// Running reports whether the watcher is polling.
func (w *Watcher) Running() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running
}

// Stats reports the watcher's liveness for the panel.
func (w *Watcher) Stats() (running bool, syncs int, lastAt time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.running, w.syncs, w.lastRep.At
}

// Touch forces the next poll to treat the tree as changed (used after an
// explicit user action, so the watcher re-baselines instead of firing).
func (w *Watcher) Touch() {
	w.mu.Lock()
	w.last = ""
	w.dirtySince = time.Time{}
	w.mu.Unlock()
}

func (w *Watcher) loop() {
	defer close(w.done)
	t := time.NewTicker(w.opts.Interval)
	defer t.Stop()
	for {
		select {
		case <-w.stop:
			return
		case <-t.C:
			w.tick()
		}
	}
}

// tick polls once: baseline drift marks the tree dirty, and a quiet period after
// the last drift triggers one sync.
func (w *Watcher) tick() {
	cur := scanFingerprint(w.opts.Roots, w.opts.MaxFiles)

	w.mu.Lock()
	changed := cur != w.last
	if changed {
		w.last = cur
		if w.dirtySince.IsZero() {
			w.dirtySince = time.Now()
		}
	}
	// Fire when the tree has gone quiet after a change (a real debounce), or
	// immediately when the change was seen and no further drift follows.
	fire := false
	if !w.dirtySince.IsZero() && time.Since(w.dirtySince) >= w.opts.Debounce {
		fire = true
		w.dirtySince = time.Time{}
	}
	w.mu.Unlock()

	if !fire {
		return
	}
	rep, err := w.hub.Sync("watch")
	if err != nil {
		return
	}
	// Adopt the post-sync fingerprint: the sync just rewrote the hub's own
	// files, and without re-baselining we would fire again on our own output
	// (when the hub happens to live under a watched root).
	w.mu.Lock()
	w.last = scanFingerprint(w.opts.Roots, w.opts.MaxFiles)
	w.syncs++
	w.lastRep = rep
	w.mu.Unlock()

	if w.onChange != nil {
		w.onChange(rep)
	}
}

// scanFingerprint stats the watched tree and hashes (path, size, mtime) for the
// files that can affect the map. It never reads file contents, and it is bounded
// by maxFiles, so the poll stays cheap even on a large repo.
func scanFingerprint(roots []string, maxFiles int) string {
	if maxFiles <= 0 {
		maxFiles = 6000
	}
	h := sha256.New()
	n := 0
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		abs := absOfPath(root)
		_ = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // an unreadable corner must not abort the scan
			}
			if d.IsDir() {
				if p != abs && watchSkipDir(d.Name()) {
					return fs.SkipDir
				}
				return nil
			}
			if !watchRelevantFile(d.Name()) {
				return nil
			}
			info, ierr := d.Info()
			if ierr != nil {
				return nil
			}
			fmt.Fprintf(h, "%s\x00%d\x00%d\x00", filepath.ToSlash(p), info.Size(), info.ModTime().UnixNano())
			n++
			if n >= maxFiles {
				return fs.SkipAll
			}
			return nil
		})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// watchSkipDir reuses the collector's skip list plus the hub's own directory, so
// the watcher never reacts to its own output.
func watchSkipDir(name string) bool {
	if skipDirs[name] {
		return true
	}
	if strings.HasPrefix(name, ".") {
		return true
	}
	return name == subdir // <root>/projectkb: our own writes
}

// watchRelevantFile reports whether a file can influence the map:
//
//   - .md / .markdown feed the docs source;
//   - .go feeds the code source (package doc comments) and go.mod / go.sum the
//     module lines;
//   - .json covers the team store and the memory index.
//
// Anything else (assets, binaries, generated bundles) is ignored, which keeps
// the scan both cheap and quiet.
func watchRelevantFile(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown", ".go", ".mod", ".sum", ".json":
		return true
	}
	return false
}
