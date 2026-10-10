package setup

import (
	"fmt"
	"os"
	"sync"
)

// RotatingLog bounds the active log plus two backups, even in long-lived runs.
type RotatingLog struct {
	mu    sync.Mutex
	path  string
	file  *os.File
	size  int64
	limit int64
}

func OpenRotatingLog(path string, limit int64) (*RotatingLog, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("invalid log size limit")
	}
	l := &RotatingLog{path: path, limit: limit}
	if err := l.open(); err != nil {
		return nil, err
	}
	return l, nil
}

func (l *RotatingLog) open() error {
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	l.file = f
	l.size = info.Size()
	return nil
}

func (l *RotatingLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	originalLen := len(p)
	if int64(len(p)) > l.limit {
		p = p[len(p)-int(l.limit):]
	}
	if l.size+int64(len(p)) > l.limit {
		_ = l.file.Close()
		_ = os.Remove(l.path + ".2")
		_ = os.Rename(l.path+".1", l.path+".2")
		if err := os.Rename(l.path, l.path+".1"); err != nil {
			// Rotation failure must not allow unbounded growth.
			if f, e := os.OpenFile(l.path, os.O_WRONLY|os.O_TRUNC, 0644); e == nil {
				f.Close()
			} else {
				return 0, err
			}
		}
		if err := l.open(); err != nil {
			return 0, err
		}
	}
	n, err := l.file.Write(p)
	l.size += int64(n)
	if err == nil {
		return originalLen, nil
	}
	return n, err
}

func (l *RotatingLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.file.Sync()
	return l.file.Close()
}
