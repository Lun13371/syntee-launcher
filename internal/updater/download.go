package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Lun13371/syntee-launcher/internal/config"
)

type DownloadResult struct {
	Path     string
	Size     int64
	SHA256   string
	Duration time.Duration
}

// Partial bytes are usable only alongside metadata matching all artifact fields.
type downloadIdentity struct {
	URL          string `json:"url"`
	Version      string `json:"version"`
	Size         int64  `json:"size"`
	Hash         string `json:"sha256"`
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
}

func saveDownloadIdentity(path string, id *downloadIdentity) error {
	b, err := json.Marshal(id)
	if err != nil {
		return err
	}
	return config.AtomicWriteFile(path, b, 0o600)
}
func removeDownloadFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// DownloadToStaging retains identity-bound .part files on interruption. Only
// size- and hash-verified complete bytes are published as update.zip. Calls for
// a given staging directory must be serialized by the caller.
func (c *Client) DownloadToStaging(ctx context.Context, m *Manifest, stagingDir string, progress func(int64, int64)) (*DownloadResult, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		return nil, err
	}
	start := time.Now()
	dest := filepath.Join(stagingDir, "update.zip")
	part, meta := dest+".part", dest+".part.json"
	hash := strings.ToLower(m.SHA256)
	// Legacy update.zip files are accepted only after full verification. Never
	// treat an unverified legacy zip as a resumable prefix.
	if info, err := os.Stat(dest); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("staged zip is not a regular file")
		}
		if info.Size() == m.ZipSize {
			sum, err := fileSHA256(dest)
			if err != nil {
				return nil, err
			}
			if sum == hash {
				return &DownloadResult{Path: dest, Size: m.ZipSize, SHA256: sum}, nil
			}
		}
		if err := removeDownloadFile(dest); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	id := downloadIdentity{URL: m.ZipURL, Version: m.Version, Size: m.ZipSize, Hash: hash}
	var old downloadIdentity
	b, err := os.ReadFile(meta)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil && json.Unmarshal(b, &old) == nil && old.URL == id.URL && old.Version == id.Version && old.Size == id.Size && old.Hash == id.Hash {
		id = old
	} else {
		if err := removeDownloadFile(part); err != nil {
			return nil, err
		}
		if err := saveDownloadIdentity(meta, &id); err != nil {
			return nil, err
		}
	}
	dlClient := updateHTTPClient(c.dlTimeout)
	defer dlClient.CloseIdleConnections()
	const maxAttempts = 6
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		retry, err := c.downloadPart(ctx, dlClient, m, part, meta, &id, progress)
		if err == nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if err := os.Rename(part, dest); err != nil {
				return nil, fmt.Errorf("publish zip: %w", err)
			}
			// Metadata is no longer needed. Publication succeeded even if cleanup fails.
			_ = os.Remove(meta)
			return &DownloadResult{Path: dest, Size: m.ZipSize, SHA256: hash, Duration: time.Since(start)}, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !retry || attempt == maxAttempts-1 {
			return nil, err
		}
		wait := time.Duration(1<<attempt)*200*time.Millisecond + time.Duration(time.Now().UnixNano()%100)*time.Millisecond
		log.Printf("download interrupted: %v; resume retry %d/%d in %s", err, attempt+2, maxAttempts, wait)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, fmt.Errorf("download retries exhausted")
}

var contentRangePattern = regexp.MustCompile(`^bytes ([0-9]+)-([0-9]+)/([0-9]+)$`)

func validDownloadRange(value string, offset, total int64) bool {
	match := contentRangePattern.FindStringSubmatch(value)
	if match == nil {
		return false
	}
	values := make([]int64, 3)
	for i := range values {
		n, err := strconv.ParseInt(match[i+1], 10, 64)
		if err != nil {
			return false
		}
		values[i] = n
	}
	return values[0] == offset && values[1] == total-1 && values[2] == total
}

// retry is true only for transient network/server errors, never disk errors.
func (c *Client) downloadPart(ctx context.Context, client *http.Client, m *Manifest, part, meta string, id *downloadIdentity, progress func(int64, int64)) (retry bool, err error) {
	f, err := os.OpenFile(part, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return false, err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			retry = false
			err = closeErr
		}
	}()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("partial is not a regular file")
	}
	offset := info.Size()
	if offset > m.ZipSize {
		if err := f.Truncate(0); err != nil {
			return false, err
		}
		offset = 0
	}
	if offset == m.ZipSize {
		return verifyDownloadPart(f, m, part)
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return false, err
	}
	if offset > 0 {
		log.Printf("resuming download: %d / %d bytes", offset, m.ZipSize)
	}
	if progress != nil {
		progress(offset, m.ZipSize)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.ZipURL, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept-Encoding", "identity")
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		if id.ETag != "" {
			req.Header.Set("If-Range", id.ETag)
		} else if id.LastModified != "" {
			req.Header.Set("If-Range", id.LastModified)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		var networkError net.Error
		transient := errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary())
		// Connection resets and premature EOF also occur before response headers.
		var operationError *net.OpError
		transient = transient || errors.As(err, &operationError) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
		return transient, fmt.Errorf("download request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return resp.StatusCode == 408 || resp.StatusCode == 429 || resp.StatusCode == 500 || resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504, fmt.Errorf("download HTTP status %d", resp.StatusCode)
	}
	if encoding := resp.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return false, fmt.Errorf("unexpected content encoding")
	}
	if resp.StatusCode == http.StatusPartialContent {
		if !validDownloadRange(resp.Header.Get("Content-Range"), offset, m.ZipSize) {
			return false, fmt.Errorf("invalid Content-Range")
		}
		if offset > 0 && ((id.ETag != "" && resp.Header.Get("ETag") != "" && resp.Header.Get("ETag") != id.ETag) || (id.ETag == "" && id.LastModified != "" && resp.Header.Get("Last-Modified") != "" && resp.Header.Get("Last-Modified") != id.LastModified)) {
			return false, fmt.Errorf("resume validator changed")
		}
	} else {
		if err := f.Truncate(0); err != nil {
			return false, err
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return false, err
		}
		offset = 0
	}
	if resp.ContentLength >= 0 && resp.ContentLength != m.ZipSize-offset {
		return false, fmt.Errorf("response size mismatch")
	}
	etag := resp.Header.Get("ETag")
	if strings.HasPrefix(etag, "W/") {
		etag = ""
	}
	if offset == 0 || etag != "" {
		id.ETag = etag
	}
	if offset == 0 || resp.Header.Get("Last-Modified") != "" {
		id.LastModified = resp.Header.Get("Last-Modified")
	}
	if err := saveDownloadIdentity(meta, id); err != nil {
		return false, err
	}
	buf := make([]byte, 256*1024)
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if int64(n) > m.ZipSize-offset {
				return false, fmt.Errorf("download exceeds manifest size")
			}
			written, err := f.Write(buf[:n])
			if err != nil {
				return false, err
			}
			if written != n {
				return false, io.ErrShortWrite
			}
			offset += int64(n)
			if progress != nil {
				progress(offset, m.ZipSize)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return true, fmt.Errorf("read download: %w", readErr)
		}
	}
	if offset != m.ZipSize {
		return true, fmt.Errorf("incomplete download: %d of %d", offset, m.ZipSize)
	}
	return verifyDownloadPart(f, m, part)
}

func verifyDownloadPart(f *os.File, m *Manifest, part string) (bool, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	hash := sha256.New()
	size, err := io.Copy(hash, f)
	if err != nil {
		return false, err
	}
	if size != m.ZipSize || hex.EncodeToString(hash.Sum(nil)) != strings.ToLower(m.SHA256) {
		// Truncate while open (Windows cannot remove an open file). Never publish
		// corrupt bytes; next invocation starts fresh with the same identity.
		if err := f.Truncate(0); err != nil {
			return false, err
		}
		return false, fmt.Errorf("download size/sha256 mismatch")
	}
	if err := f.Sync(); err != nil {
		return false, err
	}
	return false, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
