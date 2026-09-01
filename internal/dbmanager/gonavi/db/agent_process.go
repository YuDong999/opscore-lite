package db

import (
	"errors"
	"io"
	"os/exec"
	"time"
)

const agentProcessExitTimeout = 2 * time.Second

func closeAgentProcess(stdin io.Closer, cmd *exec.Cmd) error {
	if stdin != nil {
		_ = stdin.Close()
	}
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return waitForAgentExit(cmd.Wait, cmd.Process.Kill, agentProcessExitTimeout)
}

func waitForAgentExit(wait func() error, kill func() error, timeout time.Duration) error {
	if wait == nil {
		return nil
	}
	if timeout <= 0 {
		timeout = agentProcessExitTimeout
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- wait()
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-waitCh:
		return nil
	case <-timer.C:
	}

	var killErr error
	if kill != nil {
		killErr = kill()
	}

	timer.Reset(timeout)
	select {
	case <-waitCh:
		// The process is already reaped. A concurrent Kill can report access
		// denied on Windows even though cleanup completed successfully.
		return nil
	case <-timer.C:
		if killErr != nil {
			return killErr
		}
		return errors.New("driver agent process did not exit after termination")
	}
}
