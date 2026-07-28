package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// DownloadResult is what DownloadToStaging returns on success.
type DownloadResult struct {
	Path     string // absolute path to the saved zip
	Size     int64
	SHA256   string // hex-encoded, verified against manifest
	Duration time.Duration
}

// DownloadToStaging fetches the release zip into <staging>/update.zip,
// computing SHA256 as bytes arrive. If a partial staging file already exists
// and the server honours Range, the download resumes from the byte count we
// already have — this is how the launcher survives flaky connections without
// re-downloading 150MB every time.
//
// The function is idempotent: if the full file is already present and its
// SHA256 matches, it returns immediately (useful for the "download in
// background, apply later" non-force flow).
func (c *Client) DownloadToStaging(ctx context.Context, m *Manifest, stagingDir string, onProgress func(downloaded int64, total int64)) (*DownloadResult, error) {
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}
	dest := filepath.Join(stagingDir, "update.zip")

	// Fast path: already downloaded and verified.
	if info, err := os.Stat(dest); err == nil && info.Size() == m.ZipSize {
		sum, err := fileSHA256(dest)
		if err == nil && sum == m.SHA256 {
			return &DownloadResult{Path: dest, Size: info.Size(), SHA256: sum, Duration: 0}, nil
		}
	}

	// Open destination for resume: O_CREATE|O_WRONLY, append to existing bytes.
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open staging file: %w", err)
	}
	defer f.Close()

	existing, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fmt.Errorf("seek staging file: %w", err)
	}

	// If we already have the full size (or more — corrupt), restart fresh.
	if existing >= m.ZipSize {
		if err := f.Truncate(0); err != nil {
			return nil, fmt.Errorf("truncate staging file: %w", err)
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("rewind staging file: %w", err)
		}
		existing = 0
	}

	// Build the request. Add Range header only when resuming a partial file.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.ZipURL, nil)
	if err != nil {
		return nil, err
	}
	if existing > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existing))
	}

	dlClient := &http.Client{}
	if c.dlTimeout > 0 {
		dlClient.Timeout = c.dlTimeout
	}
	resp, err := dlClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download request: %w", err)
	}
	defer resp.Body.Close()

	// 200 = full body (server ignored Range or nothing was resumable); rewind.
	// 206 = partial content, server accepted our Range — continue appending.
	switch resp.StatusCode {
	case http.StatusOK:
		if existing > 0 {
			if err := f.Truncate(0); err != nil {
				return nil, fmt.Errorf("truncate on 200: %w", err)
			}
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				return nil, fmt.Errorf("rewind on 200: %w", err)
			}
			existing = 0
		}
	case http.StatusPartialContent:
		// Append from `existing`.
	default:
		return nil, fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	start := time.Now()
	sha := sha256.New()
	// If resuming, we need the SHA of the whole file — not just the new bytes.
	// Rewind, hash what we kept, then continue hashing as we write the tail.
	if existing > 0 {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("rewind for hash: %w", err)
		}
		if _, err := io.CopyN(sha, f, existing); err != nil {
			return nil, fmt.Errorf("hash existing prefix: %w", err)
		}
		if _, err := f.Seek(existing, io.SeekStart); err != nil {
			return nil, fmt.Errorf("seek to tail: %w", err)
		}
	}

	buf := make([]byte, 256*1024)
	var written int64
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return nil, fmt.Errorf("write download: %w", werr)
			}
			sha.Write(buf[:n])
			written += int64(n)
			if onProgress != nil {
				onProgress(existing+written, m.ZipSize)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("read download: %w", readErr)
		}
	}

	totalSize := existing + written
	if totalSize != m.ZipSize {
		return nil, fmt.Errorf("size mismatch: got %d, expected %d", totalSize, m.ZipSize)
	}

	got := hex.EncodeToString(sha.Sum(nil))
	if got != m.SHA256 {
		return nil, fmt.Errorf("sha256 mismatch: got %s, expected %s", got, m.SHA256)
	}

	return &DownloadResult{
		Path:     dest,
		Size:     totalSize,
		SHA256:   got,
		Duration: time.Since(start),
	}, nil
}

// fileSHA256 is a one-shot whole-file hasher used for the "already downloaded"
// fast path.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sha := sha256.New()
	if _, err := io.Copy(sha, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(sha.Sum(nil)), nil
}