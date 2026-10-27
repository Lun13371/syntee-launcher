package main

import (
	"context"
	"testing"
	"time"
)

func TestWaitChannelCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan bool)
	done := make(chan bool, 1)
	go func() { _, ok := waitChannel(ctx, ch); done <- ok }()
	cancel()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("cancelled wait succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not cancel")
	}
}

func TestUpdaterTempBootstrapHelpers(t *testing.T) {
	if hasUpdaterTempArg([]string{"--other"}) {
		t.Fatal("unexpected updater temp marker")
	}
	if !hasUpdaterTempArg([]string{"--other", updaterTempArg}) {
		t.Fatal("updater temp marker not detected")
	}
	if relaunched, err := selfInstallIfNeeded(true); err != nil || relaunched {
		t.Fatalf("temporary updater should skip self-install: relaunched=%v err=%v", relaunched, err)
	}
}

func TestWaitChannelValueAndClosed(t *testing.T) {
	ch := make(chan int, 1)
	ch <- 42
	if v, ok := waitChannel(context.Background(), ch); !ok || v != 42 {
		t.Fatalf("got %v %v", v, ok)
	}
	close(ch)
	if _, ok := waitChannel(context.Background(), ch); ok {
		t.Fatal("closed channel succeeded")
	}
}
