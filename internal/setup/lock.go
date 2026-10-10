package setup

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/windows"
)

// AcquireInstallLock serializes mutations, including self-install. Windows
// mutex ownership is thread-bound; keep the caller on this thread until release.
// A relaunched child waits until the parent returns and releases ownership.
func AcquireInstallLock(dir string) (func(), error) {
	runtime.LockOSThread()
	fail := func(err error) (func(), error) { runtime.UnlockOSThread(); return nil, err }
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(dir))))
	name, _ := windows.UTF16PtrFromString(fmt.Sprintf("Local\\SynteeInstall-%x", sum[:16]))
	h, err := windows.CreateMutex(nil, false, name)
	if err != nil && err != windows.ERROR_ALREADY_EXISTS {
		return fail(err)
	}
	result, err := windows.WaitForSingleObject(h, 30000)
	if err != nil || (result != windows.WAIT_OBJECT_0 && result != windows.WAIT_ABANDONED) {
		windows.CloseHandle(h)
		if err == nil {
			err = fmt.Errorf("another installer is running (wait result %d)", result)
		}
		return fail(err)
	}
	return func() { windows.ReleaseMutex(h); windows.CloseHandle(h); runtime.UnlockOSThread() }, nil
}
