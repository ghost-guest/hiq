// Package persistentshell runs foreground bash commands in a session-scoped
// PTY so cwd, exported variables, and shell functions survive across calls.
//
// It is invisible to models: the bash tool schema and description stay
// byte-identical. Background jobs, commands that background a child, per-call
// write-root escalations, host terminals, and PowerShell hosts keep using
// one-shot processes; see Supports.
package persistentshell
