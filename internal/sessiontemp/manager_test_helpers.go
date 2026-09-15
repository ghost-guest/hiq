package sessiontemp

import "github.com/zzycxz/fairpeer/internal/filelock"

func tryLockForTest(path string) (func(), error) {
	return filelock.Acquire(nilContext(), path)
}
