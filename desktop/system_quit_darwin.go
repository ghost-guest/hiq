//go:build darwin && cgo

package main

/*
#cgo darwin LDFLAGS: -framework Cocoa
void installhiqSystemQuitHook(void);
*/
import "C"

import "sync"

var installSystemQuitHookOnce sync.Once

func installSystemQuitHook() {
	installSystemQuitHookOnce.Do(func() {
		C.installhiqSystemQuitHook()
	})
}

//export hiqMarkSystemQuit
func hiqMarkSystemQuit() {
	markSystemQuitRequested()
}
