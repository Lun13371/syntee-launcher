package setup

import (
	"testing"
	"time"
)

func TestInstallMutexHandoff(t *testing.T) {
	dir := t.TempDir()
	release, err := AcquireInstallLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan error, 1)
	go func() {
		unlock, err := AcquireInstallLock(dir)
		if err == nil {
			unlock()
		}
		acquired <- err
	}()
	select {
	case err := <-acquired:
		release()
		t.Fatalf("second owner did not wait: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case err := <-acquired:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ownership handoff blocked")
	}
}
