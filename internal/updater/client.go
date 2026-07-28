package updater

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Lun13371/syntee-launcher/internal/config"
	"github.com/Masterminds/semver/v3"
)

// Client talks to the Syntee-Gateway updates API. It is intentionally tiny —
// the launcher only needs check + download; all the heavy lifting (zip, sha,
// swap) lives in apply.go.
type Client struct {
	http       *http.Client
	serverURL  string // e.g. "https://gateway.syntee.dev/api/public/updates"
	dlTimeout  time.Duration
}

// NewClient builds a Client from the launcher config. The check timeout is
// short (5s default) so an unreachable server never blocks app launch; the
// download timeout is generous (0 = no timeout) because releases can be large.
func NewClient(cfg *config.Config) *Client {
	checkTimeout := time.Duration(cfg.CheckTimeoutSec) * time.Second
	if checkTimeout == 0 {
		checkTimeout = 5 * time.Second
	}
	var dlTimeout time.Duration
	if cfg.DownloadTimeoutSec > 0 {
		dlTimeout = time.Duration(cfg.DownloadTimeoutSec) * time.Second
	}
	return &Client{
		http:      &http.Client{Timeout: checkTimeout},
		serverURL: cfg.UpdateServer,
		dlTimeout: dlTimeout,
	}
}

// Check asks the gateway whether `current` is older than the active release on
// `channel`. Returns (nil, nil) when the client is already up to date (HTTP
// 204). The manifest is non-nil only when an update is available.
//
// The force flag in the returned manifest already accounts for min_version:
// if current < min_version the gateway sets force=true even when the release
// itself has force=false. The launcher trusts this rather than re-comparing.
func (c *Client) Check(ctx context.Context, channel, current string) (*Manifest, error) {
	url := fmt.Sprintf("%s/check?channel=%s&current=%s", c.serverURL, channel, current)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("check request: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNoContent:
		return nil, nil // up to date
	case http.StatusOK:
		var m Manifest
		if err := decodeJSON(resp.Body, &m); err != nil {
			return nil, fmt.Errorf("decode manifest: %w", err)
		}
		return &m, nil
	default:
		return nil, fmt.Errorf("unexpected status %d from check endpoint", resp.StatusCode)
	}
}

// Download fetches the zip artifact for a specific version to dst. It supports
// HTTP Range so an interrupted download can resume from where it left off
// (the launcher writes to staging/update.zip and retries on failure).
//
// `onProgress` is called periodically with the total bytes downloaded so far;
// it may be nil.
func (c *Client) Download(ctx context.Context, zipURL string, dst io.Writer, onProgress func(downloaded int64)) error {
	// Use a separate client without the check-timeout; downloads can be slow.
	dlClient := &http.Client{}
	if c.dlTimeout > 0 {
		dlClient.Timeout = c.dlTimeout
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, zipURL, nil)
	if err != nil {
		return err
	}
	resp, err := dlClient.Do(req)
	if err != nil {
		return fmt.Errorf("download request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	buf := make([]byte, 256*1024) // 256 KiB
	var total int64
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return fmt.Errorf("write download: %w", werr)
			}
			total += int64(n)
			if onProgress != nil {
				onProgress(total)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("read download: %w", readErr)
		}
	}
	return nil
}

// IsNewer reports whether `candidate` is strictly newer than `current`.
// Both must be valid semver; on parse error we conservatively return true
// (better to attempt an update than to get stuck on a broken version string).
func IsNewer(current, candidate string) bool {
	cur, err := semver.NewVersion(current)
	if err != nil {
		return true
	}
	cand, err := semver.NewVersion(candidate)
	if err != nil {
		return false
	}
	return cand.GreaterThan(cur)
}
