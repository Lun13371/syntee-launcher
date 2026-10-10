package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProcessAliveSelf(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Fatal("self incorrectly reported dead")
	}
	if processAlive(0) {
		t.Fatal("PID zero is not a live user process")
	}
}
func TestImageUnder(t *testing.T) {
	for _, test := range []struct {
		image string
		want  bool
	}{
		{`C:\Install\current\Client.exe`, true},
		{`c:\install\CURRENT\sub\helper.exe`, true},
		{`C:\Install\current-other\Client.exe`, false},
		{`D:\Install\current\Client.exe`, false},
		{`C:\Install\current\..\other.exe`, false},
	} {
		if got := imageUnder(`C:\Install\current`, test.image); got != test.want {
			t.Errorf("%s: %v", test.image, got)
		}
	}
}
func TestEnumerationExcludesSelf(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pids, err := ProcessesIn(filepath.Dir(exe))
	if err != nil {
		t.Fatal(err)
	}
	for _, pid := range pids {
		if pid == uint32(os.Getpid()) {
			t.Fatal("self included")
		}
	}
}
