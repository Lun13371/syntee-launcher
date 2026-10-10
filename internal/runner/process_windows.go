package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return !os.IsNotExist(err) && err != windows.ERROR_INVALID_PARAMETER
	}
	defer windows.CloseHandle(h)
	result, err := windows.WaitForSingleObject(h, 0)
	return err != nil || result == uint32(windows.WAIT_TIMEOUT)
}

// ProcessesIn enumerates real process image paths, not executable basenames.
// Self is always excluded, even when running as current/updater.exe.
func ProcessesIn(dir string) ([]uint32, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	var pids []uint32
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if entry.ProcessID == uint32(os.Getpid()) {
			continue
		}
		h, openErr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, entry.ProcessID)
		if openErr != nil {
			continue
		}
		buf := make([]uint16, 32768)
		size := uint32(len(buf))
		queryErr := windows.QueryFullProcessImageName(h, 0, &buf[0], &size)
		windows.CloseHandle(h)
		if queryErr == nil && imageUnder(dir, windows.UTF16ToString(buf[:size])) {
			pids = append(pids, entry.ProcessID)
		}
	}
	if err != windows.ERROR_NO_MORE_FILES {
		return nil, err
	}
	return pids, nil
}

func imageUnder(dir, image string) bool {
	rel, err := filepath.Rel(strings.ToLower(filepath.Clean(dir)), strings.ToLower(filepath.Clean(image)))
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// StopProcessesIn offers the optional client quit-marker protocol first, then
// targets only validated PIDs. No /IM fallback may kill another installation.
func StopProcessesIn(ctx context.Context, dir string, grace time.Duration) error {
	pids, err := ProcessesIn(dir)
	if err != nil {
		return err
	}
	if len(pids) == 0 {
		return nil
	}
	marker := filepath.Join(dir, ".launcher_quit")
	if err := os.WriteFile(marker, []byte("quit"), 0644); err != nil {
		return err
	}
	defer os.Remove(marker)
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		pids, err = ProcessesIn(dir)
		if err != nil {
			return err
		}
		if len(pids) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	pids, err = ProcessesIn(dir)
	if err != nil {
		return err
	}
	for _, pid := range pids {
		// Recheck immediately before taskkill to reduce the PID-reuse window.
		current, err := ProcessesIn(dir)
		if err != nil {
			return err
		}
		found := false
		for _, p := range current {
			if p == pid {
				found = true
			}
		}
		if !found {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		// Terminate through a validated handle, not /T: our own updater can be
		// a descendant of the client and must survive. Enumerated in-tree helpers
		// are terminated separately; unrelated descendants are not touched.
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pid)
		if err != nil {
			if !processAlive(int(pid)) {
				continue
			}
			return err
		}
		buf := make([]uint16, 32768)
		size := uint32(len(buf))
		err = windows.QueryFullProcessImageName(h, 0, &buf[0], &size)
		if err == nil && imageUnder(dir, windows.UTF16ToString(buf[:size])) {
			err = windows.TerminateProcess(h, 1)
			if err == nil {
				result, waitErr := windows.WaitForSingleObject(h, 5000)
				if waitErr != nil {
					err = waitErr
				} else if result == uint32(windows.WAIT_TIMEOUT) {
					err = fmt.Errorf("timeout stopping PID %d", pid)
				}
			}
		}
		windows.CloseHandle(h)
		if err != nil && processAlive(int(pid)) {
			return fmt.Errorf("stop PID %d: %w", pid, err)
		}
	}
	return nil
}
