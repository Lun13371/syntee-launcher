package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Lun13371/syntee-launcher/internal/config"
	"github.com/Masterminds/semver/v3"
)

// Client talks to the Syntee-Gateway updates API. It is intentionally tiny —
// the launcher only needs check + download; all the heavy lifting (zip, sha,
// swap) lives in apply.go.
type Client struct {
	http      *http.Client
	serverURL string // e.g. "https://syntee.fun/api/public/updates"
	dlTimeout time.Duration
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
		http:      updateHTTPClient(checkTimeout),
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
	if !channelPattern.MatchString(channel) {
		return nil, fmt.Errorf("invalid channel")
	}
	if err := validateUpdateURL(c.serverURL); err != nil {
		return nil, err
	}
	endpoint, err := url.Parse(strings.TrimRight(c.serverURL, "/") + "/check")
	if err != nil {
		return nil, err
	}
	query := endpoint.Query()
	query.Set("channel", channel)
	query.Set("current", current)
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
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
		dec := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
		if err := dec.Decode(&m); err != nil {
			return nil, fmt.Errorf("decode manifest: %w", err)
		}
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("manifest has trailing data")
		}
		// Some gateway manifests still advertise the artifact URL as HTTP even
		// though the same endpoint is available over HTTPS. Upgrade only a
		// same-host URL from an HTTPS update server; never change an arbitrary
		// download host or relax validateUpdateURL's HTTPS requirement.
		m.ZipURL = upgradeSameHostArtifactURL(c.serverURL, m.ZipURL)
		if err := m.Validate(channel); err != nil {
			return nil, err
		}
		return &m, nil
	default:
		return nil, fmt.Errorf("unexpected status %d from check endpoint", resp.StatusCode)
	}
}

// Download fetches a complete artifact into a writer. Use DownloadToStaging
// for identity-bound resumable downloads.
//
// `onProgress` is called periodically with the total bytes downloaded so far;
// it may be nil.
func (c *Client) Download(ctx context.Context, zipURL string, dst io.Writer, onProgress func(downloaded int64)) error {
	// Use a separate client without the check-timeout; downloads can be slow.
	if err := validateUpdateURL(zipURL); err != nil {
		return err
	}
	dlClient := updateHTTPClient(c.dlTimeout)
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

// updateHTTPClient bounds connection establishment and response headers, while
// allowing long downloads. Per-read deadlines also bound stalled response bodies.
func updateHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = 15 * time.Second
	transport.DisableCompression = true
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &idleReadConn{Conn: conn}, nil
	}
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		return validateUpdateURL(req.URL.String())
	}}
}

type idleReadConn struct{ net.Conn }

func (c *idleReadConn) Read(p []byte) (int, error) {
	if err := c.SetReadDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return 0, err
	}
	return c.Conn.Read(p)
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
