package connection

import (
	"path/filepath"
	"syscall"
)

// Perform Windows user name.
func userCurrent() (string, error) {
	pw_name := make([]uint16, 128)
	pwname_size := uint32(len(pw_name)) - 1
	err := syscall.GetUserNameEx(syscall.NameSamCompatible, &pw_name[0], &pwname_size)
	if err != nil {
		return "", ErrCouldNotDetectUsername
	}
	s := syscall.UTF16ToString(pw_name)
	u := filepath.Base(s)
	return u, nil
}
