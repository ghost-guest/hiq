// Package permission decides, per tool call, whether to allow it, deny it, or
// ask the user first. The core is a pure Policy (rule evaluation, no I/O); a
// Gate wraps a Policy with an optional interactive Approver and is what the
// agent consults at execute time. Keeping rule evaluation pure makes it
// trivially testable and keeps the agent independent of how "ask" is resolved.
package permission

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
)

// Decision is the outcome of evaluating a tool call against a Policy.
type Decision int

const (
	// Allow runs the tool without prompting.
	Allow Decision = iota
	// Ask defers to an interactive Approver (or, with none, resolves to Allow).
	Ask
	// Deny blocks the tool in every mode.
	Deny
)

func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case Ask:
		return "ask"
	case Deny:
		return "deny"
	default:
		return "unknown"
	}
}

// ParseDecision maps a config string to a Decision. Unknown / empty input
// defaults to Ask — the conservative posture for a writer fallback.
func ParseDecision(s string) Decision {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "allow":
		return Allow
	case "deny":
		return Deny
	default:
		return Ask
	}
}

// Rule matches tool calls. Tool is the tool name; Subject, when non-empty,
// constrains the call's subject. A glob Subject (see matchGlob) matches by
// wildcard; a Literal Subject matches by exact string equality. An empty Subject
// matches every call to Tool.
type Rule struct {
	Tool    string
	Subject string
	// Literal matches Subject by exact equality rather than as a glob, so a
	// remembered concrete command keeps any '*'/'?' as ordinary characters
	// instead of turning them into wildcards.
	Literal bool
}

// ParseRule parses "ToolName", "ToolName(glob)", or the legacy
// "ToolName=literal" form. Surrounding whitespace is trimmed. The "=literal"
// form (taken when the '=' precedes any '(') matches the rest of the string
// verbatim — no globbing — and is kept for existing configs that were written
// before the Claude Code-style Tool(specifier) approval rules. ok is false for
// a malformed entry (empty tool name) so the caller can warn rather than
// silently install a rule that matches nothing.
func ParseRule(s string) (Rule, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Rule{}, false
	}
	if eq := strings.IndexByte(s, '='); eq > 0 {
		if paren := strings.IndexByte(s, '('); paren < 0 || eq < paren {
			tool := strings.TrimSpace(s[:eq])
			if tool == "" {
				return Rule{}, false
			}
			return Rule{Tool: tool, Subject: s[eq+1:], Literal: true}, true
		}
	}
	if i := strings.IndexByte(s, '('); i >= 0 && strings.HasSuffix(s, ")") {
		tool := strings.TrimSpace(s[:i])
		if tool == "" {
			return Rule{}, false
		}
		return Rule{Tool: tool, Subject: s[i+1 : len(s)-1]}, true
	}
	return Rule{Tool: s}, true
}

func parseRules(ss []string) []Rule {
	var out []Rule
	for _, s := range ss {
		if r, ok := ParseRule(s); ok {
			out = append(out, r)
		}
	}
	return out
}

// Policy is a set of rules plus the writer fallback mode. It is the pure,
// I/O-free heart of the permission layer.
type Policy struct {
	// Mode is the fallback decision for writer tools when no rule matches.
	// Read-only tools always fall back to Allow.
	Mode  Decision
	Allow []Rule
	Ask   []Rule
	Deny  []Rule
	// HardDeny is an immutable deny layer that cannot be overridden by user
	// config. It is checked FIRST, before Allow/Ask/Deny, so even a user who
	// configures "*": "allow" cannot bypass these rules. Used by plan mode to
	// guarantee writer tools stay blocked during planning regardless of
	// session/runtime permission changes. Set via SetHardDeny (not New, which
	// leaves it nil = no hard rules).
	HardDeny []Rule
}

// New builds a Policy from config string slices and a mode string ("ask" by
// default). Malformed rule strings are dropped. HardDeny is left empty; call
// SetHardDeny to install immutable rules (e.g. for plan mode).
func New(mode string, allow, ask, deny []string) Policy {
	return Policy{
		Mode:  ParseDecision(mode),
		Allow: parseRules(allow),
		Ask:   parseRules(ask),
		Deny:  parseRules(deny),
	}
}

// SetHardDeny installs immutable deny rules. These are checked before all
// other rules and cannot be overridden. Returns the modified Policy for
// chaining. Used by the controller to enforce plan-mode read-only as a
// data-layer guarantee (not just a runtime boolean).
func (p Policy) SetHardDeny(rules []string) Policy {
	p.HardDeny = parseRules(rules)
	return p
}

// Decide evaluates a tool call. readOnly is the tool's own classification; args
// is the raw JSON the model sent, from which the call's subject is extracted
// for glob matching. Calls with multiple subjects must be safe for every subject
// before the call is allowed. Precedence: deny > ask > allow > fallback (Allow
// for readers, Mode for writers).
func (p Policy) Decide(toolName string, readOnly bool, args json.RawMessage) Decision {
	return p.DecideSubjects(toolName, readOnly, subjectsFor(toolName, args))
}

// subjectsFor extracts the approval subject(s) for a tool call. For most tools
// this is the generic key-based Subjects() (command/path/pattern). A few tools
// whose subject isn't one of those keys get bespoke extraction:
//   - email_send: subject is the recipient domains (e.g. "gmail.com"), so a
//     user can allow-list their company domain once and only get prompted for
//     unfamiliar recipients. "to" can be a string or an array.
//   - rag_delete: subject is the collection name, so a user can approve
//     deleting a scratch collection while still being prompted for production
//     knowledge bases.
//
// These are the IRREVERSIBLE, outward-facing coWork operations where HITL
// adds real value (an email sent or a KB deleted can't be undone). Read-only
// and reversible operations fall through to the generic path and their normal
// policy.
func subjectsFor(toolName string, args json.RawMessage) []string {
	switch toolName {
	case "email_send":
		return emailSubjects(args)
	case "rag_delete":
		return ragDeleteSubjects(args)
	}
	return Subjects(args)
}

// emailSubjects extracts recipient domains from email_send's "to", "cc", and
// "bcc" fields. Each field may be a single string ("a@x.com"), a comma-
// separated string ("a@x.com, b@y.com"), or a JSON array (["a@x.com"]). We
// collapse each to its domain so a bare "email_send" ask rule (no subject)
// still prompts, while a "email_send:example.com" rule allows a whole domain.
// Duplicates are dropped.
//
// SECURITY: cc and bcc MUST be included alongside "to" — email_send actually
// delivers to all three, so a deny rule scoped to a domain is bypassable if we
// only inspect "to" (an attacker sets to=colleague@company.com while hiding
// exfil@evil.com in bcc). See internal audit finding A1.
func emailSubjects(args json.RawMessage) []string {
	if len(args) == 0 {
		return nil
	}
	var p struct {
		To  json.RawMessage `json:"to"`
		Cc  json.RawMessage `json:"cc"`
		Bcc json.RawMessage `json:"bcc"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return nil
	}

	var addrs []string
	for _, raw := range []json.RawMessage{p.To, p.Cc, p.Bcc} {
		addrs = append(addrs, parseRecipientField(raw)...)
	}

	seen := map[string]bool{}
	var out []string
	for _, a := range addrs {
		d := emailDomain(a)
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		out = append(out, d)
	}
	// If no domains parsed, return a single empty-subject marker so a bare
	// "email_send" rule still matches and prompts.
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// parseRecipientField decodes one recipient field (to/cc/bcc) — a JSON array
// of addresses, a single string, or a comma-separated string — into a flat
// list of address strings.
func parseRecipientField(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	// Try array first.
	var addrs []string
	if err := json.Unmarshal(raw, &addrs); err == nil {
		return addrs
	}
	// Fall back to a single string (possibly comma-separated).
	var single string
	if err := json.Unmarshal(raw, &single); err != nil {
		return nil
	}
	return splitRecipients(single)
}

// splitRecipients splits a comma-separated recipient string, tolerating
// whitespace and stray separators.
func splitRecipients(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if a := strings.TrimSpace(p); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// emailDomain extracts the lowercase domain from an email address
// ("user@example.com" → "example.com"). Returns "" for malformed input.
func emailDomain(addr string) string {
	addr = strings.TrimSpace(addr)
	at := strings.LastIndex(addr, "@")
	if at < 0 || at == len(addr)-1 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(addr[at+1:]))
}

// ragDeleteSubjects extracts the collection name from rag_delete's args. An
// empty collection (delete-everything) maps to the wildcard subject so a bare
// "rag_delete" rule still prompts.
func ragDeleteSubjects(args json.RawMessage) []string {
	if len(args) == 0 {
		return nil
	}
	var p struct {
		Collection string `json:"collection"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return nil
	}
	if strings.TrimSpace(p.Collection) == "" {
		return []string{"*"}
	}
	return []string{p.Collection}
}

// DecideSubjects evaluates a tool call when the caller already extracted one or
// more approval subjects from args. If any subject is denied, the whole call is
// denied; if any requires approval, the call asks; only when every subject is
// allowed does the call proceed without prompting.
func (p Policy) DecideSubjects(toolName string, readOnly bool, subjects []string) Decision {
	if len(subjects) == 0 {
		return p.DecideSubject(toolName, readOnly, "")
	}
	out := Allow
	for _, subject := range subjects {
		switch p.DecideSubject(toolName, readOnly, subject) {
		case Deny:
			return Deny
		case Ask:
			out = Ask
		}
	}
	return out
}

// DecideSubject evaluates a tool call when the caller already extracted the
// stable approval subject from args.
func (p Policy) DecideSubject(toolName string, readOnly bool, subject string) Decision {
	switch {
	// HardDeny is checked FIRST and cannot be overridden by any other rule.
	// This is the data-layer guarantee that plan mode (and any future
	// security boundary) relies on: even "*": "allow" can't bypass it.
	case matchAny(p.HardDeny, toolName, subject):
		return Deny
	case matchAny(p.Deny, toolName, subject):
		return Deny
	case matchAny(p.Ask, toolName, subject):
		return Ask
	case matchAny(p.Allow, toolName, subject):
		return Allow
	case readOnly:
		return Allow
	default:
		return p.Mode
	}
}

// matchAny reports whether any rule matches the (toolName, subject) pair. A
// subject-specific rule cannot match a call that exposes no subject.
func matchAny(rules []Rule, toolName, subject string) bool {
	for _, r := range rules {
		if !ruleToolMatches(r.Tool, toolName) {
			continue
		}
		if r.Subject == "" {
			return true
		}
		if subject == "" {
			continue
		}
		if ruleSubjectMatches(r, subject) {
			return true
		}
	}
	return false
}

// RuleMatchesString reports whether one config-style rule string matches the
// given tool subject. It is used for session grants as well as persisted config
// rules so both paths share identical matching semantics.
func RuleMatchesString(rule, toolName, subject string) bool {
	r, ok := ParseRule(rule)
	return ok && matchAny([]Rule{r}, toolName, subject)
}

// RuleCoversString reports whether every call represented by candidate is
// already covered by existing. It intentionally proves only the cases hiq
// creates automatically: exact rules covered by broader globs or bare tool
// rules, exact duplicate globs, and bare tool rules covering subject rules.
func RuleCoversString(existing, candidate string) bool {
	a, ok := ParseRule(existing)
	if !ok {
		return false
	}
	b, ok := ParseRule(candidate)
	if !ok {
		return false
	}
	if !ruleToolCompatible(a.Tool, b.Tool) {
		return false
	}
	if a.Subject == "" {
		return true
	}
	if b.Subject == "" {
		return false
	}
	if bashRulePrefixBaseMatches(a, b) {
		return true
	}
	if b.Literal || !hasGlobMeta(b.Subject) {
		return ruleSubjectMatches(a, b.Subject)
	}
	return !a.Literal && a.Subject == b.Subject
}

func hasGlobMeta(s string) bool {
	return strings.ContainsAny(s, "*?")
}

func bashRulePrefixBaseMatches(existing, candidate Rule) bool {
	if canonicalRuleTool(existing.Tool) != "bash" || canonicalRuleTool(candidate.Tool) != "bash" {
		return false
	}
	existingBase, ok := bashPrefixBase(existing.Subject)
	if !ok {
		return false
	}
	candidateBase, ok := bashPrefixBase(candidate.Subject)
	return ok && existingBase == candidateBase
}

// subjectKeys are the JSON argument keys, in priority order, that carry a tool
// call's "subject" — the thing a Subject glob matches against. Generic so tools
// need not implement a permission-specific method: bash exposes command, the
// file tools expose path / file_path, grep exposes pattern.
var subjectKeys = []string{"command", "file_path", "path", "pattern"}

// pathSubjectKeys are the subject keys that carry a filesystem path (as opposed
// to a bash command or a grep pattern). These are normalized with filepath.Clean
// before matching so a deny rule like edit_file(secrets/*) cannot be evaded by
// the agent supplying "./secrets/key.pem" or "secrets//key.pem" — the raw value
// would not match the glob. See security audit finding A3.
var pathSubjectKeys = map[string]bool{"file_path": true, "path": true}

// Subjects extracts all matchable subject strings from a call's raw JSON args.
// A call may return multiple entries when several keys are present, so
// permission evaluation can check every endpoint.
func Subjects(args json.RawMessage) []string {
	if len(args) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(args, &m); err != nil {
		return nil
	}
	var out []string
	for _, k := range subjectKeys {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				// Normalize filesystem paths so "./secrets/x" or "secrets//x"
				// matches a deny rule written as "secrets/*". Bash commands and
				// grep patterns are NOT paths — leave them verbatim.
				//
				// ToSlash is applied after Clean so the subject uses forward
				// slashes on every platform — the same convention rule strings
				// and matchGlob use. Without it, Windows filepath.Clean yields
				// "secrets\key.pem" which fails to match the glob "secrets/*".
				if pathSubjectKeys[k] {
					s = filepath.ToSlash(filepath.Clean(s))
				}
				out = append(out, s)
			}
		}
	}
	return out
}

// Subject extracts the first matchable subject string from a call's raw JSON
// args, returning "" when none of the known keys is present (such a call only
// matches bare "ToolName" rules).
func Subject(args json.RawMessage) string {
	subjects := Subjects(args)
	if len(subjects) > 0 {
		return subjects[0]
	}
	return ""
}

// matchGlob reports whether name matches pattern, where '*' matches any run of
// characters (including separators) and '?' matches exactly one. Unlike
// path.Match, '*' is not stopped by '/', which is what command-line and path
// prefixes ("rm -rf*", "/etc/*") intuitively expect. Linear time with
// backtracking, byte-oriented.
func matchGlob(pattern, name string) bool {
	var px, nx, starPx, starNx int
	starPx = -1
	for nx < len(name) {
		switch {
		case px < len(pattern) && (pattern[px] == '?' || pattern[px] == name[nx]):
			px++
			nx++
		case px < len(pattern) && pattern[px] == '*':
			starPx = px
			starNx = nx
			px++
		case starPx != -1:
			px = starPx + 1
			starNx++
			nx = starNx
		default:
			return false
		}
	}
	for px < len(pattern) && pattern[px] == '*' {
		px++
	}
	return px == len(pattern)
}

// Approver resolves an Ask decision interactively. Implementations live in the
// front-end (the chat TUI); a non-interactive run passes a nil Approver, which
// the Gate treats as "allow" to preserve autonomous behaviour.
type Approver interface {
	// Approve asks the user about a pending call. It returns whether to allow
	// it and whether to remember that choice as a new rule. A non-nil err (e.g.
	// the context was cancelled while waiting) aborts the turn.
	Approve(ctx context.Context, toolName, subject string, args json.RawMessage) (allow, remember bool, err error)
}

// Gate is what the agent consults at execute time: a Policy plus an optional
// Approver. It satisfies the agent's Gate interface structurally.
type Gate struct {
	Policy   Policy
	Approver Approver

	// RiskOverrides is a per-tool risk-class map (e.g. {"mcp__srv__get": RiskRead})
	// populated from config ([[plugins]] risk). It lets a trusted MCP server's
	// read-only tool skip approval without a code change. Nil = use defaults
	// (MCP tools external, builtins per the risk table).
	RiskOverrides map[string]RiskClass

	// OnRemember, when set, is invoked with a new allow rule the user chose to
	// remember (e.g. "Bash(go build)"), so the front-end can persist it.
	OnRemember func(rule string)
}

// NewGate wires a Policy to an Approver (nil for non-interactive use).
func NewGate(p Policy, a Approver) *Gate { return &Gate{Policy: p, Approver: a} }

// Check decides whether a tool call may run. It is the method the agent's Gate
// interface expects. A denied or refused call returns allow=false with a short
// reason the agent feeds back to the model.
func (g *Gate) Check(ctx context.Context, toolName string, args json.RawMessage, readOnly bool) (bool, string, error) {
	if toolName == "bash" && !readOnly {
		subject := Subject(args)
		if isReadOnlyBashSubject(subject) {
			readOnly = true
		}
	}
	switch g.Policy.Decide(toolName, readOnly, args) {
	case Deny:
		return false, "denied by permission policy — this tool/command is on the deny list. Do not retry it; choose another approach or stop and explain.", nil
	case Ask:
		if g.Approver == nil {
			// Headless / non-interactive mode (no UI to prompt the user).
			// For ordinary tools we allow (preserve autonomy — the agent can
			// still read/grep/build freely). But for external-risk operations
			// (email_send, rag_delete, and MCP tools by default) we deny: there
			// is no human to confirm, and silently sending email / deleting a
			// knowledge base / calling an external API from an unattended
			// scheduled task is too risky to default-allow. A user who wants
			// these in headless mode must add an explicit allow rule in config,
			// or mark the MCP server's risk as read/exec. See security review
			// finding #1 and SPEC v2 §3.2A.
			if IsExternal(toolName, readOnly, g.RiskOverrides) {
				return false, "denied in headless mode — " + toolName + " is an external-risk operation (outward/irreversible) and there is no interactive user to approve it; add an explicit allow rule in config, or for MCP tools set the server's risk to read/exec", nil
			}
			return true, "", nil // non-interactive: preserve autonomy
		}
		subject := Subject(args)
		allow, remember, err := g.Approver.Approve(ctx, toolName, subject, args)
		if err != nil {
			return false, "approval aborted", err
		}
		if !allow {
			return false, "the user declined this tool call — do not retry it; ask how they would like to proceed or choose another approach.", nil
		}
		if remember && g.OnRemember != nil {
			// "Always allow" is tool-wide: persist the bare tool name so any
			// later subject (a different file / command) is allowed without
			// re-prompting. Deny rules still take precedence on every call.
			g.OnRemember(toolName)
			// Also add the rule to the in-memory Policy immediately so it
			// takes effect in the current session without requiring a restart.
			// The session-level grant (controller.granted) already covers the
			// Approver path, but any code path that consults Policy.Decide()
			// directly would miss the rule until the next controller build.
			if rule, ok := ParseRule(toolName); ok {
				g.Policy.Allow = append(g.Policy.Allow, rule)
			}
		}
		return true, "", nil
	default:
		return true, "", nil
	}
}

// RememberRuleForScope builds the rule string persisted when the user chooses
// an always-allow option. Bash commands prefer a safe prefix (go test:*) so
// similar invocations (different search terms, different test packages) match;
// when no safe prefix can be extracted the exact command is used. File
// mutation tools are always remembered tool-wide (Edit). Other tools use their
// bare tool name. Deny rules still take precedence on every call.
func RememberRuleForScope(toolName, subject string) string {
	subject = strings.TrimSpace(subject)
	if subject != "" && toolName == "bash" {
		if pattern := BashCommandPrefix(subject); pattern != "" {
			return "Bash(" + pattern + ")"
		}
		return "Bash(" + subject + ")"
	}
	if IsFileMutationTool(toolName) {
		return "Edit"
	}
	return toolName
}

// SessionGrantKey returns the in-memory rule for "allow this session". Bash
// prefers a command prefix when one is available, falling back to the exact
// command when unsafe. File mutation tools share a single Edit grant.
func SessionGrantKey(toolName, subject string) string {
	return SessionGrantRuleForScope(toolName, subject)
}

// SessionGrantRuleForScope returns the in-memory rule for a session grant.
// Bash prefers a command prefix when one is available; file mutation tools
// share a single Edit grant; all other tools return the bare tool name.
func SessionGrantRuleForScope(toolName, subject string) string {
	subject = strings.TrimSpace(subject)
	if toolName == "bash" && subject != "" {
		if pattern := BashCommandPrefix(subject); pattern != "" {
			return "Bash(" + pattern + ")"
		}
		return "Bash(" + subject + ")"
	}
	if IsFileMutationTool(toolName) {
		return "Edit"
	}
	return toolName
}

// BashCommandPrefix returns a conservative prefix rule for "similar command"
// approvals. It avoids shell syntax and keeps the prefix at command-word
// boundaries, so approving "go test ./..." grants "go test:*" rather than a
// broader "go *".
func BashCommandPrefix(subject string) string {
	cmd := strings.TrimSpace(subject)
	if cmd == "" || containsShellSyntax(cmd) {
		return ""
	}
	if BashDangerWarning(cmd) != "" {
		return ""
	}
	fields := strings.Fields(cmd)
	if len(fields) < 2 {
		return ""
	}
	base := strings.ToLower(fields[0])
	if isPackageManagerRun(base) && len(fields) >= 3 && strings.ToLower(fields[1]) == "run" {
		return fields[0] + " " + fields[1] + " " + fields[2] + ":*"
	}
	return fields[0] + " " + fields[1] + ":*"
}

func isPackageManagerRun(base string) bool {
	switch base {
	case "npm", "pnpm", "yarn", "bun":
		return true
	default:
		return false
	}
}

// IsFileMutationTool reports whether a built-in tool mutates workspace files.
func IsFileMutationTool(toolName string) bool {
	switch toolName {
	case "write_file", "edit_file", "multi_edit":
		return true
	default:
		return false
	}
}

func ruleToolMatches(ruleTool, toolName string) bool {
	ruleTool = canonicalRuleTool(ruleTool)
	return ruleTool == toolName || (ruleTool == "file_mutation" && IsFileMutationTool(toolName))
}

func ruleToolCompatible(existingTool, candidateTool string) bool {
	existingTool = canonicalRuleTool(existingTool)
	candidateTool = canonicalRuleTool(candidateTool)
	return existingTool == candidateTool ||
		(existingTool == "file_mutation" && (candidateTool == "file_mutation" || IsFileMutationTool(candidateTool)))
}

func canonicalRuleTool(toolName string) string {
	switch strings.TrimSpace(toolName) {
	case "Bash", "bash":
		return "bash"
	case "Edit", "edit", "file_mutation",
		"write_file", "edit_file", "multi_edit": // the concrete builtin tool names
		return "file_mutation"
	default:
		return toolName
	}
}

func ruleSubjectMatches(rule Rule, subject string) bool {
	if rule.Subject == "" {
		return true
	}
	if subject == "" {
		return false
	}
	if rule.Literal {
		return rule.Subject == subject
	}
	if canonicalRuleTool(rule.Tool) == "bash" {
		if base, ok := bashColonPrefixBase(rule.Subject); ok {
			return bashPrefixMatches(base, subject)
		}
		if base, ok := legacyBashSpaceStarPrefixBase(rule.Subject); ok {
			return bashPrefixMatches(base, subject)
		}
	}
	return matchGlob(rule.Subject, subject)
}

func bashColonPrefixBase(pattern string) (string, bool) {
	if !strings.HasSuffix(pattern, ":*") {
		return "", false
	}
	base := strings.TrimSuffix(pattern, ":*")
	return base, base != ""
}

func legacyBashSpaceStarPrefixBase(pattern string) (string, bool) {
	if !strings.HasSuffix(pattern, " *") {
		return "", false
	}
	base := strings.TrimSuffix(pattern, " *")
	return base, base != ""
}

func bashPrefixBase(pattern string) (string, bool) {
	if base, ok := bashColonPrefixBase(pattern); ok {
		return base, true
	}
	return legacyBashSpaceStarPrefixBase(pattern)
}

func bashPrefixMatches(base, subject string) bool {
	if containsShellSyntax(subject) {
		return false
	}
	return subject == base || strings.HasPrefix(subject, base+" ")
}
