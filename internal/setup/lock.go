package setup

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// ErrLockBusy means another process owned the mutex for the whole timeout.
// It must be distinguished from a real failure: the normal reaction is
// "an update is already in progress", not "something is broken".
var ErrLockBusy = errors.New("another installer is running")

// defaultInstallLockTimeout is how long a user-facing launcher waits for a
// concurrent mutation before giving up. Long mutations (download + extract)
// legitimately hold the lock, but a dialog that appears 30s after a double
// click looks like a hang.
const defaultInstallLockTimeout = 30 * time.Second

// mutexName builds a session-local mutex name. Local\ keeps the namespace
// per logon session, so fast user switching and other accounts never collide.
func mutexName(prefix, dir string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(dir))))
	return fmt.Sprintf("Local\\%s-%x", prefix, sum[:16])
}

// acquireNamed waits up to timeout for a named mutex.
//
// Windows mutex ownership is thread-bound, so the calling goroutine is pinned
// to the current OS thread until release runs: acquire and release must be
// invoked from the same goroutine, and no other goroutine may call release.
func acquireNamed(name string, timeout time.Duration) (func(), error) {
	runtime.LockOSThread()
	fail := func(err error) (func(), error) { runtime.UnlockOSThread(); return nil, err }
	ptr, _ := windows.UTF16PtrFromString(name)
	h, err := windows.CreateMutex(nil, false, ptr)
	if err != nil && err != windows.ERROR_ALREADY_EXISTS {
		return fail(err)
	}
	result, err := windows.WaitForSingleObject(h, uint32(timeout/time.Millisecond))
	if err != nil {
		windows.CloseHandle(h)
		return fail(err)
	}
	switch result {
	case uint32(windows.WAIT_OBJECT_0), uint32(windows.WAIT_ABANDONED):
		// WAIT_ABANDONED: the previous owner died without releasing, so
		// ownership is now ours and the protected state may be inconsistent —
		// callers run recovery right after acquiring for exactly this reason.
	case uint32(windows.WAIT_TIMEOUT):
		windows.CloseHandle(h)
		return fail(fmt.Errorf("%w (waited %s, wait result %d)", ErrLockBusy, timeout, result))
	default:
		windows.CloseHandle(h)
		return fail(fmt.Errorf("wait for mutex failed (wait result %d)", result))
	}
	return func() { windows.ReleaseMutex(h); windows.CloseHandle(h); runtime.UnlockOSThread() }, nil
}

// AcquireInstallLock serializes mutations of the installation directory:
// install, update apply, recovery and uninstall.
//
// Hold it only around the mutation itself. It used to be held for the whole
// GUI session (deferred from main), so any second launch — or the client's
// restart-to-update flow — failed with "another installer is running" while
// the first window merely waited for a click.
func AcquireInstallLock(dir string) (func(), error) {
	return AcquireInstallLockTimeout(dir, defaultInstallLockTimeout)
}

// AcquireInstallLockTimeout is AcquireInstallLock with an explicit budget.
// A background updater spawned by the client should wait longer than a
// user-facing window, because nobody is watching it fail.
func AcquireInstallLockTimeout(dir string, timeout time.Duration) (func(), error) {
	return acquireNamed(mutexName("SynteeInstall", dir), timeout)
}

// AcquireSingleInstance guards the user-facing launcher window. It is
// separate from the install mutex on purpose:
//
//   - this one is held for the whole process lifetime and prevents two
//     launcher windows;
//   - the install mutex is held only during mutations and prevents the
//     launcher and a client-spawned updater.exe from writing at the same time.
//
// A busy single-instance mutex is not an error: it returns (nil, false, nil)
// so the caller can focus the existing window and exit quietly.
func AcquireSingleInstance(dir string) (func(), bool, error) {
	release, err := acquireNamed(mutexName("SynteeSingle", dir), 0)
	if err != nil {
		if errors.Is(err, ErrLockBusy) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return release, true, nil
}
