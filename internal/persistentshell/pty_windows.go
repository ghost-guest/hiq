//go:build windows

package persistentshell

import (
	"sync"

	"github.com/UserExistsError/conpty"
	"golang.org/x/sys/windows"
)

type windowsPTY struct {
	pty       *conpty.ConPty
	closeOnce sync.Once
}

func startPTY(argv []string, dir string, env []string) (ptyConn, error) {
	if len(argv) == 0 {
		return nil, errEmptyArgv
	}
	if !conpty.IsConPtyAvailable() {
		return nil, conpty.ErrConPtyUnsupported
	}
	commandLine := windows.ComposeCommandLine(argv)
	p, err := conpty.Start(
		commandLine,
		conpty.ConPtyDimensions(80, 24),
		conpty.ConPtyWorkDir(dir),
		conpty.ConPtyEnv(env),
	)
	if err != nil {
		return nil, err
	}
	return &windowsPTY{pty: p}, nil
}

func (c *windowsPTY) Read(p []byte) (int, error) {
	return c.pty.Read(p)
}

func (c *windowsPTY) Write(p []byte) (int, error) {
	return c.pty.Write(p)
}

func (c *windowsPTY) Close() error {
	var closeErr error
	c.closeOnce.Do(func() {
		if c.pty != nil {
			closeErr = c.pty.Close()
		}
	})
	return closeErr
}
