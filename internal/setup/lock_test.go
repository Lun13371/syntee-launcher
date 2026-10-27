package setup

import (
	"errors"
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

// A busy install mutex must surface as ErrLockBusy once the budget runs out,
// so callers can say "an update is already in progress" instead of dumping a
// raw "wait result 258" at the user.
func TestInstallLockTimeoutReportsBusy(t *testing.T) {
	dir := t.TempDir()
	release, err := AcquireInstallLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	result := make(chan error, 1)
	go func() {
		unlock, err := AcquireInstallLockTimeout(dir, 50*time.Millisecond)
		if err == nil {
			unlock()
		}
		result <- err
	}()

	select {
	case err := <-result:
		if !errors.Is(err, ErrLockBusy) {
			t.Fatalf("want ErrLockBusy, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("AcquireInstallLockTimeout did not honour its budget")
	}
}

// The single-instance guard must never block: a second launch reports
// "not acquired" immediately so it can focus the existing window and exit,
// instead of sitting on a 30s wait and then showing an error.
func TestSingleInstanceIsNonBlocking(t *testing.T) {
	dir := t.TempDir()
	release, acquired, err := AcquireSingleInstance(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("first AcquireSingleInstance did not acquire")
	}
	defer release()

	done := make(chan struct{})
	go func() {
		defer close(done)
		rel, ok, err := AcquireSingleInstance(dir)
		if err != nil {
			t.Errorf("second AcquireSingleInstance: %v", err)
			return
		}
		if ok {
			rel()
			t.Error("second AcquireSingleInstance must not acquire")
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("AcquireSingleInstance blocked instead of returning immediately")
	}
}

// The two mutexes are independent: holding the window guard must not prevent a
// mutation (the client's updater.exe runs while a launcher window is open).
func TestSingleInstanceDoesNotBlockInstallLock(t *testing.T) {
	dir := t.TempDir()
	releaseSingle, acquired, err := AcquireSingleInstance(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Fatal("single instance not acquired")
	}
	defer releaseSingle()

	release, err := AcquireInstallLockTimeout(dir, time.Second)
	if err != nil {
		t.Fatalf("install lock blocked by the single-instance guard: %v", err)
	}
	release()
}
