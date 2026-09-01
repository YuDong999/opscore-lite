//go:build !(darwin || dragonfly || freebsd || linux || netbsd || openbsd || windows)

package appdata

import "os"

type bootstrapFileLock struct {
	file *os.File
}

func acquireBootstrapFileLock(path string) (*bootstrapFileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	return &bootstrapFileLock{file: file}, nil
}

func (lock *bootstrapFileLock) Close() error {
	if lock == nil || lock.file == nil {
		return nil
	}
	err := lock.file.Close()
	lock.file = nil
	return err
}

func atomicReplaceBootstrapFile(source string, target string) error {
	return os.Rename(source, target)
}
