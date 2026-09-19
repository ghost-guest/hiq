package builtin

import (
	"errors"
	"runtime"
	"testing"
)

func TestIsWindowsProcInitFailure(t *testing.T) {
	if !isWindowsProcInitFailure(errors.New("command exited: exit status 0xc0000142")) {
		t.Fatal("0xc0000142 should classify as a process-init failure")
	}
	if isWindowsProcInitFailure(errors.New("command exited: exit status 1")) {
		t.Fatal("normal exit must not classify as init failure")
	}
	if isWindowsProcInitFailure(nil) {
		t.Fatal("nil must not classify")
	}
	if runtime.GOOS != "windows" {
		t.Log("non-windows host: classification is always false there, by design")
	}
}
