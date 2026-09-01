package db

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestWaitForAgentExitIgnoresKillErrorAfterProcessExit(t *testing.T) {
	waitStarted := make(chan struct{})
	processExited := make(chan struct{})
	killErr := errors.New("TerminateProcess: Access is denied")

	err := waitForAgentExit(
		func() error {
			close(waitStarted)
			<-processExited
			return nil
		},
		func() error {
			<-waitStarted
			close(processExited)
			return killErr
		},
		50*time.Millisecond,
	)
	if err != nil {
		t.Fatalf("process exit should make the racing kill error irrelevant, got %v", err)
	}
}

func TestWaitForAgentExitWaitsBeforeKilling(t *testing.T) {
	var killCalled atomic.Bool

	err := waitForAgentExit(
		func() error { return nil },
		func() error {
			killCalled.Store(true)
			return nil
		},
		time.Second,
	)
	if err != nil {
		t.Fatalf("graceful process exit returned error: %v", err)
	}
	if killCalled.Load() {
		t.Fatal("process was killed even though it exited during the graceful wait")
	}
}

func TestWaitForAgentExitReturnsKillErrorWhenProcessDoesNotExit(t *testing.T) {
	waitStarted := make(chan struct{})
	processExited := make(chan struct{})
	killErr := errors.New("TerminateProcess: Access is denied")

	err := waitForAgentExit(
		func() error {
			close(waitStarted)
			<-processExited
			return nil
		},
		func() error {
			<-waitStarted
			return killErr
		},
		20*time.Millisecond,
	)
	close(processExited)
	if !errors.Is(err, killErr) {
		t.Fatalf("expected kill error when the process never exits, got %v", err)
	}
}
