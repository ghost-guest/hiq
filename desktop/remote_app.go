package main

// remote_app.go — Wails-bound entry points for remote workspaces: the wizard's
// probe/browse calls, OpenRemoteTab, and the remote branches of the
// local-filesystem assumptions (list/read/search/git/reveal) that live in
// App methods keyed off the active tab.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/control"
	"github.com/zzycxz/hiq/internal/remotehost"
)

// activeRemoteSession returns the active tab's remoteSession, nil for local
// tabs (the local FS branch runs then).
func (a *App) activeRemoteSession() *remoteSession {
	a.mu.RLock()
	tab := a.activeTabLocked()
	a.mu.RUnlock()
	if tab == nil || tab.Remote == nil {
		return nil
	}
	if rs, ok := tab.Ctrl.(*remoteSession); ok {
		return rs
	}
	return nil
}

// --- wizard bindings ----------------------------------------------------------

// RemoteProbeResult is what the wizard's connecting step learns about a host.
type RemoteProbeResult struct {
	Version string `json:"version"`
	Goos    string `json:"goos"`
	Arch    string `json:"arch"`
	// HomeDir is a sensible default root for the directory picker (WSL: the
	// selected user's home).
	HomeDir string `json:"homeDir"`
}

// RemoteConnectProbe dials the host for a ref (provisioning the binary on
// first use) and opens a browsing session rooted at "/" so the wizard's
// directory picker can navigate the whole remote filesystem.
func (a *App) RemoteConnectProbe(kind, target, user string) (RemoteProbeResult, error) {
	ref := RemoteRef{Kind: strings.TrimSpace(kind), Target: strings.TrimSpace(target), User: strings.TrimSpace(user)}
	if ref.Kind == "" || ref.Target == "" {
		return RemoteProbeResult{}, fmt.Errorf("kind and target are required")
	}
	return a.remoteConnectProbe(ref)
}

// RemoteBrowseList lists one remote directory for the wizard picker. path is
// absolute on the remote side; the browsing session is rooted at /.
func (a *App) RemoteBrowseList(path string) ([]remotehost.FsEntry, error) {
	a.mu.RLock()
	m := a.remoteManager
	a.mu.RUnlock()
	if m == nil {
		return nil, fmt.Errorf("no remote connection")
	}
	m.mu.Lock()
	var link *remoteHostLink
	for _, ml := range m.links {
		link = ml.link
		break
	}
	m.mu.Unlock()
	if link == nil {
		return nil, fmt.Errorf("no remote connection")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		path = "/"
	}
	var res remotehost.FsListResult
	if err := link.call(a.bootContext(), "fs/list", remotehost.FsListParams{SessionID: "remote-wizard", Path: relToWizardRoot(path)}, &res); err != nil {
		return nil, err
	}
	return res.Entries, nil
}

// relToWizardRoot converts an absolute remote path into the relative form the
// host's escape guard expects for a session rooted at "/".
func relToWizardRoot(abs string) string {
	abs = strings.TrimPrefix(abs, "/")
	if abs == "" {
		return ""
	}
	return abs
}

// RemoteWizardClose tears down the wizard browsing sessions and retires links
// left with no live sessions (an abandoned wizard must not keep host processes
// running).
func (a *App) RemoteWizardClose() {
	a.mu.RLock()
	m := a.remoteManager
	a.mu.RUnlock()
	if m == nil {
		return
	}
	m.mu.Lock()
	type linkWithSessions struct {
		ml   *managedLink
		link *remoteHostLink
	}
	var links []linkWithSessions
	for _, ml := range m.links {
		links = append(links, linkWithSessions{ml: ml, link: ml.link})
	}
	m.mu.Unlock()
	for _, l := range links {
		if s := l.link.session("remote-wizard"); s != nil {
			s.Close()
		}
		l.ml.mu.Lock()
		idle := len(l.ml.sessions) == 0 && !l.ml.dead
		l.ml.mu.Unlock()
		if idle {
			m.mu.Lock()
			delete(m.links, remoteRefKey(l.ml.ref))
			m.mu.Unlock()
			l.link.close()
		}
	}
}

// OpenRemoteTab opens (or activates) a tab for a remote workspace root. The
// project is registered in the active profile's index under a remote slug key
// (never colliding with local paths), and the controller attaches over the
// host link in buildTabController's remote branch.
func (a *App) OpenRemoteTab(kind, target, user, root, label string) (TabMeta, error) {
	ref := RemoteRef{Kind: strings.TrimSpace(kind), Target: strings.TrimSpace(target), User: strings.TrimSpace(user)}
	root = strings.TrimSpace(root)
	if ref.Kind == "" || ref.Target == "" || root == "" {
		return TabMeta{}, fmt.Errorf("kind, target and root are required")
	}
	if !strings.HasPrefix(root, "/") {
		return TabMeta{}, fmt.Errorf("remote root must be an absolute path")
	}
	if ref.Label == "" {
		ref.Label = ref.Target + " · " + root
	}

	profile := a.activeProfileKey()

	// Dedupe: reuse an open tab on the same ref+root.
	a.mu.Lock()
	for _, tab := range a.tabs {
		if tab.Remote != nil && remoteRefKey(*tab.Remote) == remoteRefKey(ref) && tab.WorkspaceRoot == root {
			a.activeTabID = tab.ID
			meta := a.tabMeta(tab, true)
			a.mu.Unlock()
			return meta, nil
		}
	}
	tabID := a.newUniqueTabIDLocked()
	tab := &WorkspaceTab{
		ID:               tabID,
		Scope:            "project",
		WorkspaceRoot:    root,
		Remote:           &ref,
		TopicTitle:       label,
		profile:          profile,
		mode:             "normal",
		toolApprovalMode: control.ToolApprovalAsk,
		disabledMCP:      map[string]ServerView{},
	}
	tab.sink = &tabEventSink{tabID: tabID, app: a, ctx: a.ctx}
	a.tabs[tabID] = tab
	a.tabOrder = append(a.tabOrder, tabID)
	a.activeTabID = tabID
	a.saveTabsLocked()
	meta := a.tabMeta(tab, true)
	a.mu.Unlock()

	registerRemoteProject(ref, root, label, profile)
	a.emitProjectTreeChanged()
	a.startTabControllerBuild(tab)
	return meta, nil
}

// registerRemoteProject records the remote workspace in the remote registry
// (remote-projects.json). The profile's projects.json is deliberately NOT
// written: its entries are local roots, and a slug key there renders nowhere
// (the tree's empty-project guard filters it) — stale slug entries from older
// builds are equally inert, so no cleanup migration is needed.
func registerRemoteProject(ref RemoteRef, root, title, profile string) {
	_ = profile
	if title == "" {
		title = ref.Label
	}
	upsertRemoteProject(ref, root, title)
}

// --- local-FS assumption branches ----------------------------------------------

// remoteListDir backs the @-mention menu for remote tabs.
func (a *App) remoteListDir(rs *remoteSession, rel string) []DirEntry {
	var res remotehost.FsListResult
	if err := rs.call("fs/list", remotehost.FsListParams{SessionID: rs.id, Path: strings.ReplaceAll(strings.TrimPrefix(rel, "/"), "\\", "/")}, &res); err != nil {
		return []DirEntry{}
	}
	out := make([]DirEntry, 0, len(res.Entries))
	for _, e := range res.Entries {
		out = append(out, DirEntry{Name: e.Name, IsDir: e.Dir})
	}
	return out
}

// remoteSearchFileRefs backs @-file search for remote tabs.
func (a *App) remoteSearchFileRefs(rs *remoteSession, query string) []DirEntry {
	var res remotehost.FsSearchResult
	if err := rs.call("fs/search", remotehost.FsSearchParams{SessionID: rs.id, Query: query}, &res); err != nil {
		return nil
	}
	var results []struct {
		Path  string
		IsDir bool
	}
	if err := jsonUnmarshalRaw(res.Results, &results); err != nil {
		return nil
	}
	out := make([]DirEntry, 0, len(results))
	for _, r := range results {
		out = append(out, DirEntry{Name: r.Path, IsDir: r.IsDir})
	}
	return out
}

// remoteReadFile backs file previews for remote tabs: media comes back as a
// data URL, text as a trimmed body.
func (a *App) remoteReadFile(rs *remoteSession, rel string) FilePreview {
	out := FilePreview{Path: rel}
	rel = strings.ReplaceAll(strings.TrimPrefix(strings.TrimSpace(rel), "/"), "\\", "/")
	var res remotehost.FsReadResult
	if err := rs.call("fs/read", remotehost.FsReadParams{SessionID: rs.id, Path: rel}, &res); err != nil {
		out.Err = err.Error()
		return out
	}
	switch res.Kind {
	case "missing":
		out.Err = "file not found"
	case "text":
		out.Body = res.Text
		out.Truncated = res.Truncated
	case "binary":
		out.Binary = true
	default:
		// image/pdf/video/audio: the data URL renders in an <img>/<video> tag
		// the same way attachment URLs already do.
		out.Kind = res.Kind
		out.Mime = res.Mime
		out.URL = res.DataURL
	}
	out.Size = res.Size
	return out
}

// remoteWorkspaceChanges backs the git dock for remote tabs: porcelain entries
// arrive as one code per path.
func (a *App) remoteWorkspaceChanges(rs *remoteSession) WorkspaceChangesView {
	var res remotehost.GitStatusResult
	if err := rs.call("git/status", remotehost.SessionRef{SessionID: rs.id}, &res); err != nil {
		return WorkspaceChangesView{GitAvailable: false, GitErr: err.Error()}
	}
	if !res.IsRepo {
		return WorkspaceChangesView{GitAvailable: false, GitErr: "not a git repository"}
	}
	byPath := map[string]*workspaceChangeAccumulator{}
	for _, e := range res.Entries {
		path := strings.TrimPrefix(strings.TrimSpace(e.Path), "./")
		if path == "" {
			continue
		}
		status := "M"
		switch {
		case strings.Contains(e.Change, "A"), strings.Contains(e.Change, "?"):
			status = "A"
		case strings.Contains(e.Change, "D"):
			status = "D"
		}
		if byPath[path] == nil {
			byPath[path] = &workspaceChangeAccumulator{view: WorkspaceChangeView{Path: path, GitStatus: status}}
		}
	}
	// Session (checkpoint) changes overlay git status for remote tabs too.
	for _, meta := range rs.Checkpoints() {
		for _, p := range meta.Paths {
			path := strings.TrimPrefix(strings.TrimSpace(p), "./")
			if path == "" {
				continue
			}
			if byPath[path] == nil {
				byPath[path] = &workspaceChangeAccumulator{view: WorkspaceChangeView{Path: path}}
			}
			byPath[path].hasSession = true
			acc := byPath[path]
			if len(acc.view.Turns) == 0 || acc.view.Turns[len(acc.view.Turns)-1] != meta.Turn {
				acc.view.Turns = append(acc.view.Turns, meta.Turn)
			}
		}
	}
	paths := make([]string, 0, len(byPath))
	for p := range byPath {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	changes := make([]WorkspaceChangeView, 0, len(paths))
	for _, p := range paths {
		changes = append(changes, byPath[p].view)
	}
	branch := res.Branch
	if res.Detached {
		branch = res.Branch + " (detached)"
	}
	untracked := 0
	for _, e := range res.Entries {
		if strings.HasPrefix(e.Change, "?") {
			untracked++
		}
	}
	return WorkspaceChangesView{GitAvailable: true, GitBranch: branch, Files: changes, Added: res.Added, Removed: res.Removed, Untracked: untracked}
}

// remoteReveal opens the remote path's Windows UNC form in Explorer (WSL only).
func (a *App) remoteReveal(rs *remoteSession, rel string) error {
	if rs.ref.Kind != "wsl" {
		return fmt.Errorf("no local file manager for %s workspaces", rs.ref.Kind)
	}
	rel = strings.ReplaceAll(strings.TrimPrefix(strings.TrimSpace(rel), "/"), "\\", "/")
	root := strings.TrimSuffix(rs.root, "/")
	linuxPath := root + "/" + rel
	unc := wslDistroUNC(rs.ref.Target, linuxPath)
	cmd := exec.Command("explorer.exe", unc)
	return cmd.Start()
}

func jsonUnmarshalRaw(raw []byte, v any) error {
	return json.Unmarshal(raw, v)
}

// remoteStateForTab derives the TabMeta badge state: connected while a live
// link serves the session, offline after a drop, "" while never built.
func remoteStateForTab(a *App, tab *WorkspaceTab) string {
	if tab.Remote == nil || a.remoteManager == nil {
		return ""
	}
	if tab.Ctrl == nil {
		if tab.StartupErr != "" {
			return "offline"
		}
		return "connecting"
	}
	a.remoteManager.mu.Lock()
	ml := a.remoteManager.links[remoteRefKey(*tab.Remote)]
	a.remoteManager.mu.Unlock()
	if ml == nil {
		return "offline"
	}
	ml.mu.Lock()
	dead := ml.dead
	ml.mu.Unlock()
	if dead {
		return "offline"
	}
	return "connected"
}

// SetSSHSecret stores an SSH credential secret (password or passphrase) in
// the encrypted secret store BEFORE SSHConnect is called — secrets never ride
// the bridge as SSHConnect parameters (2026-08-21 security hardening).
func (a *App) SetSSHSecret(authField, host, port, user, value string) error {
	authField = strings.ToLower(strings.TrimSpace(authField))
	if authField != "password" && authField != "passphrase" {
		return fmt.Errorf("authField must be password or passphrase")
	}
	store := desktopSecretStore()
	if store == nil {
		return fmt.Errorf("secret store unavailable")
	}
	passwordKey, passphraseKey := sshCredsSecretKeys(host, port, user)
	key := passwordKey
	if authField == "passphrase" {
		key = passphraseKey
	}
	return store.Set(key, value)
}

// SSHConnect is the wizard's SSH entry. Secrets (password/passphrase) are read
// from the secret store — the frontend calls SetSSHSecret first; they never
// arrive as plain parameters over the bridge.
func (a *App) SSHConnect(host, port, user, authMethod, keyPath string) (RemoteProbeResult, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return RemoteProbeResult{}, fmt.Errorf("ssh host is required")
	}
	authMethod = strings.ToLower(strings.TrimSpace(authMethod))
	if authMethod != "password" && authMethod != "privatekey" && authMethod != "privateKey" {
		authMethod = "password"
	}
	if authMethod == "privatekey" {
		authMethod = "privateKey"
	}
	// Read secrets from the encrypted store — absent entries mean "" (the
	// caller may have skipped the secret step for key-only auth).
	password, passphrase := "", ""
	if store := desktopSecretStore(); store != nil {
		passwordKey, passphraseKey := sshCredsSecretKeys(host, port, user)
		if v, _, err := store.Get(passwordKey); err == nil {
			password = v
		}
		if v, _, err := store.Get(passphraseKey); err == nil {
			passphrase = v
		}
	}
	creds := &sshCredentials{
		Host: host, Port: strings.TrimSpace(port), User: strings.TrimSpace(user),
		AuthMethod: authMethod, Password: password,
		KeyPath: strings.TrimSpace(keyPath), Passphrase: passphrase,
	}
	if a.remoteManager == nil {
		a.remoteManager = &remoteHostManager{app: a}
	}
	a.remoteManager.saveSSHCredentials(creds)
	ref := RemoteRef{Kind: "ssh", Target: sshTarget(creds.Host, creds.Port), User: creds.User, KeyPath: creds.KeyPath, Label: "ssh · " + sshTarget(creds.Host, creds.Port)}
	return a.remoteConnectProbe(ref)
}

// remoteConnectProbe is the shared probe body behind RemoteConnectProbe and
// SSHConnect.
func (a *App) remoteConnectProbe(ref RemoteRef) (RemoteProbeResult, error) {
	if a.remoteManager == nil {
		a.remoteManager = &remoteHostManager{app: a}
	}
	ctx, cancel := context.WithTimeout(a.bootContext(), 3*time.Minute)
	defer cancel()
	link, err := a.remoteManager.ensureLink(ctx, ref)
	if err != nil {
		return RemoteProbeResult{}, err
	}
	wizardID := "remote-wizard"
	if link.session(wizardID) == nil {
		if _, err := newRemoteSession(ctx, link, ref, remotehost.SessionNewParams{SessionID: wizardID, Cwd: "/"}); err != nil {
			return RemoteProbeResult{}, err
		}
	}
	var hello remotehost.HelloResult
	if err := link.call(ctx, "host/hello", map[string]any{}, &hello); err != nil {
		return RemoteProbeResult{}, err
	}
	home := ""
	switch strings.ToLower(ref.Kind) {
	case "wsl":
		home, _ = wslHomeForProbe(ref.Target, ref.User)
	case "docker":
		home, _ = dockerHomeForProbe(ref.Target)
	}
	a.emitRemoteStatus(ref, "connected")
	return RemoteProbeResult{Version: hello.Version, Goos: hello.Goos, Arch: hello.Arch, HomeDir: home}, nil
}

// ServerConnect is the wizard's Server entry: record the token (manager cache
// + secret store), then run the standard probe against the running host.
func (a *App) ServerConnect(address, token string, useTLS bool) (RemoteProbeResult, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		return RemoteProbeResult{}, fmt.Errorf("server address is required")
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		address = address + ":8787"
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return RemoteProbeResult{}, fmt.Errorf("server token is required")
	}
	if a.remoteManager == nil {
		a.remoteManager = &remoteHostManager{app: a}
	}
	a.remoteManager.saveServerToken(address, token)
	ref := RemoteRef{Kind: "server", Target: address, TLS: useTLS, Label: "server · " + address}
	return a.remoteConnectProbe(ref)
}

// ServerForget clears a Server connection's stored token and TLS certificate
// pin (after a server re-provisioned its certificate, or to retire a host).
func (a *App) ServerForget(address string) error {
	address = strings.TrimSpace(address)
	if address == "" {
		return fmt.Errorf("server address is required")
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		address = address + ":8787"
	}
	if store := desktopSecretStore(); store != nil {
		_ = store.Delete(serverTokenKey(address))
		_ = store.Delete(serverPinKey(address))
	}
	if a.remoteManager != nil {
		a.remoteManager.mu.Lock()
		delete(a.remoteManager.serverTokens, remoteRefKey(RemoteRef{Kind: "server", Target: address}))
		a.remoteManager.mu.Unlock()
	}
	return nil
}

// OpenRemoteTopicTab opens a remote workspace tab pinned to a specific topic's
// newest host-side session (the project tree's remote topic click).
func (a *App) OpenRemoteTopicTab(kind, target, user string, tls bool, root, topicID, title, sessionPath string) (TabMeta, error) {
	ref := RemoteRef{
		Kind:   strings.TrimSpace(kind),
		Target: strings.TrimSpace(target),
		User:   strings.TrimSpace(user),
		TLS:    tls,
	}
	root = strings.TrimSpace(root)
	if ref.Kind == "" || ref.Target == "" || root == "" {
		return TabMeta{}, fmt.Errorf("kind, target and root are required")
	}
	if ref.Label == "" {
		ref.Label = ref.Target + " · " + root
	}
	// Resolve the newest session for the topic when the caller didn't pin one.
	if sessionPath == "" {
		for _, t := range remoteTopicsForRef(a.remoteManager, ref, root) {
			if t.TopicID == topicID {
				sessionPath = t.NewestSession
				break
			}
		}
	}
	profile := a.activeProfileKey()

	a.mu.Lock()
	// Reuse an open tab on the same ref+root+topic.
	for _, tab := range a.tabs {
		if tab.Remote != nil && remoteRefKey(*tab.Remote) == remoteRefKey(ref) && tab.WorkspaceRoot == root && tab.TopicID == topicID {
			a.activeTabID = tab.ID
			meta := a.tabMeta(tab, true)
			a.mu.Unlock()
			return meta, nil
		}
	}
	tabID := a.newUniqueTabIDLocked()
	tab := &WorkspaceTab{
		ID:               tabID,
		Scope:            "project",
		WorkspaceRoot:    root,
		Remote:           &ref,
		TopicID:          strings.TrimSpace(topicID),
		TopicTitle:       strings.TrimSpace(title),
		SessionPath:      strings.TrimSpace(sessionPath),
		profile:          profile,
		mode:             "normal",
		toolApprovalMode: control.ToolApprovalAsk,
		disabledMCP:      map[string]ServerView{},
	}
	tab.sink = &tabEventSink{tabID: tabID, app: a, ctx: a.ctx}
	a.tabs[tabID] = tab
	a.tabOrder = append(a.tabOrder, tabID)
	a.activeTabID = tabID
	a.saveTabsLocked()
	meta := a.tabMeta(tab, true)
	a.mu.Unlock()
	a.startTabControllerBuild(tab)
	return meta, nil
}
