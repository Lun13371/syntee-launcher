package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testManifest(url, data string) *Manifest {
	hash := sha256.Sum256([]byte(data))
	return &Manifest{Version: "1.2.3", Channel: "stable", ZipURL: url, ZipSize: int64(len(data)), SHA256: hex.EncodeToString(hash[:])}
}
func seedPartial(t *testing.T, dir string, m *Manifest, prefix string, etag string) {
	t.Helper()
	dest := filepath.Join(dir, "update.zip.part")
	if err := os.WriteFile(dest, []byte(prefix), 0600); err != nil {
		t.Fatal(err)
	}
	if err := saveDownloadIdentity(dest+".json", &downloadIdentity{URL: m.ZipURL, Version: m.Version, Size: m.ZipSize, Hash: m.SHA256, ETag: etag}); err != nil {
		t.Fatal(err)
	}
}
func assertPublished(t *testing.T, dir, data string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "update.zip"))
	if err != nil || string(b) != data {
		t.Fatalf("published %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "update.zip.part")); !os.IsNotExist(err) {
		t.Fatalf("part still exists: %v", err)
	}
}

func TestInterruptedDownloadResumes(t *testing.T) {
	data := strings.Repeat("abcdef", 1000)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		w.Header().Set("ETag", `"release"`)
		if n == 1 {
			w.Header().Set("Content-Length", fmt.Sprint(len(data)))
			w.WriteHeader(200)
			_, _ = w.Write([]byte(data[:1200]))
			w.(http.Flusher).Flush()
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		if r.Header.Get("Range") != "bytes=1200-" || r.Header.Get("If-Range") != `"release"` {
			t.Errorf("resume headers: %v", r.Header)
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 1200-%d/%d", len(data)-1, len(data)))
		w.WriteHeader(206)
		_, _ = w.Write([]byte(data[1200:]))
	}))
	defer server.Close()
	dir := t.TempDir()
	_, err := (&Client{}).DownloadToStaging(context.Background(), testManifest(server.URL, data), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatal(requests.Load())
	}
	assertPublished(t, dir, data)
}

func TestResumeResponses(t *testing.T) {
	for _, mode := range []string{"valid", "ignored", "wrong-start", "wrong-total", "missing-range", "changed-validator", "hash"} {
		t.Run(mode, func(t *testing.T) {
			data := "abcdefghij"
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("Range") != "bytes=3-" {
					t.Errorf("range %q", r.Header.Get("Range"))
				}
				if mode == "ignored" {
					_, _ = w.Write([]byte(data))
					return
				}
				value := "bytes 3-9/10"
				switch mode {
				case "wrong-start":
					value = "bytes 2-9/10"
				case "wrong-total":
					value = "bytes 3-9/11"
				case "missing-range":
					value = ""
				case "changed-validator":
					w.Header().Set("ETag", `"new"`)
				}
				w.Header().Set("Content-Range", value)
				w.WriteHeader(206)
				if mode == "hash" {
					_, _ = w.Write([]byte("XXXXXXX"))
				} else {
					_, _ = w.Write([]byte(data[3:]))
				}
			}))
			defer server.Close()
			dir := t.TempDir()
			m := testManifest(server.URL, data)
			seedPartial(t, dir, m, data[:3], `"old"`)
			_, err := (&Client{}).DownloadToStaging(context.Background(), m, dir, nil)
			if mode == "valid" || mode == "ignored" {
				if err != nil {
					t.Fatal(err)
				}
				assertPublished(t, dir, data)
			} else {
				if err == nil {
					t.Fatal("accepted invalid response")
				}
				if _, err := os.Stat(filepath.Join(dir, "update.zip")); !os.IsNotExist(err) {
					t.Fatal("published invalid zip")
				}
			}
			if requests.Load() != 1 {
				t.Fatalf("retried permanent error: %d", requests.Load())
			}
		})
	}
}

func TestChangedIdentityDiscardsPrefix(t *testing.T) {
	for _, field := range []string{"url", "version", "size", "hash"} {
		t.Run(field, func(t *testing.T) {
			data := "new artifact"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Range") != "" {
					t.Error("resumed wrong identity")
				}
				_, _ = w.Write([]byte(data))
			}))
			defer server.Close()
			m := testManifest(server.URL, data)
			old := *m
			switch field {
			case "url":
				old.ZipURL += "/old"
			case "version":
				old.Version = "1.0.0"
			case "size":
				old.ZipSize++
			case "hash":
				old.SHA256 = strings.Repeat("0", 64)
			}
			dir := t.TempDir()
			seedPartial(t, dir, &old, "wrong", "")
			_, err := (&Client{}).DownloadToStaging(context.Background(), m, dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			assertPublished(t, dir, data)
		})
	}
}

func TestCancelRetainsAndResumesAcrossCalls(t *testing.T) {
	data := strings.Repeat("x", 1000000)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var offset int
		if r.Header.Get("Range") != "" {
			_, _ = fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-", &offset)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, len(data)-1, len(data)))
			w.WriteHeader(206)
		}
		_, _ = w.Write([]byte(data[offset:]))
	}))
	defer server.Close()
	dir := t.TempDir()
	m := testManifest(server.URL, data)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := (&Client{}).DownloadToStaging(ctx, m, dir, func(n, total int64) {
		if n > 0 {
			cancel()
		}
	})
	if err != context.Canceled {
		t.Fatalf("cancel: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "update.zip.part"))
	if err != nil || info.Size() == 0 {
		t.Fatalf("lost prefix %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("retried cancellation")
	}
	_, err = (&Client{}).DownloadToStaging(context.Background(), m, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPublished(t, dir, data)
}

func TestLegacyZipCompatibility(t *testing.T) {
	for _, valid := range []bool{true, false} {
		t.Run(fmt.Sprint(valid), func(t *testing.T) {
			data := "verified zip"
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Header.Get("Range") != "" {
					t.Error("resumed legacy file")
				}
				_, _ = w.Write([]byte(data))
			}))
			defer server.Close()
			dir := t.TempDir()
			legacy := data
			if !valid {
				legacy = "bad"
			}
			if err := os.WriteFile(filepath.Join(dir, "update.zip"), []byte(legacy), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := (&Client{}).DownloadToStaging(context.Background(), testManifest(server.URL, data), dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			assertPublished(t, dir, data)
			expected := int32(1)
			if valid {
				expected = 0
			}
			if calls.Load() != expected {
				t.Fatal(calls.Load())
			}
		})
	}
}

func TestExhaustedNetworkRetainsPartial(t *testing.T) {
	data := "abcdefghij"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		offset := 0
		if r.Header.Get("Range") != "" {
			_, _ = fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-", &offset)
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(data)-offset))
		if offset > 0 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-9/10", offset))
			w.WriteHeader(206)
		}
		_, _ = w.Write([]byte(data[offset : offset+1]))
		w.(http.Flusher).Flush()
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			conn.Close()
		}
	}))
	defer server.Close()
	dir := t.TempDir()
	_, err := (&Client{}).DownloadToStaging(context.Background(), testManifest(server.URL, data), dir, nil)
	if err == nil || calls.Load() != 6 {
		t.Fatalf("retry bound: %v, %d", err, calls.Load())
	}
	b, err := os.ReadFile(filepath.Join(dir, "update.zip.part"))
	if err != nil || string(b) != data[:6] {
		t.Fatalf("lost partial: %q %v", b, err)
	}
}

func TestLastModifiedIfRange(t *testing.T) {
	data := "abcdefghij"
	date := "Wed, 21 Oct 2015 07:28:00 GMT"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-Range") != date {
			t.Errorf("If-Range: %q", r.Header.Get("If-Range"))
		}
		w.Header().Set("Content-Range", "bytes 3-9/10")
		w.Header().Set("Last-Modified", date)
		w.WriteHeader(206)
		_, _ = w.Write([]byte(data[3:]))
	}))
	defer server.Close()
	dir := t.TempDir()
	m := testManifest(server.URL, data)
	seedPartial(t, dir, m, data[:3], "")
	err := saveDownloadIdentity(filepath.Join(dir, "update.zip.part.json"), &downloadIdentity{URL: m.ZipURL, Version: m.Version, Size: m.ZipSize, Hash: m.SHA256, LastModified: date})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&Client{}).DownloadToStaging(context.Background(), m, dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertPublished(t, dir, data)
}

func TestRetryPolicy(t *testing.T) {
	for _, status := range []int{403, 404, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(status) }))
			defer server.Close()
			_, err := (&Client{}).DownloadToStaging(context.Background(), testManifest(server.URL, "abc"), t.TempDir(), nil)
			if err == nil {
				t.Fatal("expected error")
			}
			expected := int32(1)
			if status == 503 {
				expected = 6
			}
			if calls.Load() != expected {
				t.Fatalf("calls %d", calls.Load())
			}
		})
	}
	t.Run("backoff cancellation", func(t *testing.T) {
		reached := make(chan struct{}, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503); reached <- struct{}{} }))
		defer server.Close()
		ctx, cancel := context.WithCancel(context.Background())
		go func() { <-reached; cancel() }()
		start := time.Now()
		_, err := (&Client{}).DownloadToStaging(ctx, testManifest(server.URL, "abc"), t.TempDir(), nil)
		if err != context.Canceled || time.Since(start) > time.Second {
			t.Fatalf("backoff not cancellable: %v", err)
		}
	})
	t.Run("disk failure", func(t *testing.T) {
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
		defer server.Close()
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "update.zip"), 0700); err != nil {
			t.Fatal(err)
		}
		_, err := (&Client{}).DownloadToStaging(context.Background(), testManifest(server.URL, "abc"), dir, nil)
		if err == nil || calls.Load() != 0 {
			t.Fatalf("disk error: %v, calls %d", err, calls.Load())
		}
	})
}
