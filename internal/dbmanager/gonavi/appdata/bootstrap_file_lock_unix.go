//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd

package appdata

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type bootstrapFileLock struct {
	file *os.File
}

func acquireBootstrapFileLock(path string) (*bootstrapFileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		_ = file.Close()
		return nil, err
	}
	_ = file.Chmod(0o600)
	return &bootstrapFileLock{file: file}, nil
}

func (lock *bootstrapFileLock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	unlockErr := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN)
	closeErr := lock.file.Close()
	lock.file = nil
	return errors.Join(unlockErr, closeErr)
}

func atomicReplaceBootstrapFile(source string, target string) error {
	if err := os.Rename(source, target); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(target))
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
