// Package updater handles checking the gateway for new client releases,
// downloading the zip artifact (with HTTP Range resume), verifying its SHA256
// and applying it atomically to current/.
package updater

import (
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
)

var channelPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// Validate rejects malformed metadata before it reaches the filesystem or network.
// An expected channel may be supplied by Check; downloads validate the manifest
// independently without assuming a particular deployment's channel names.
func (m *Manifest) Validate(expectedChannel ...string) error {
	if m == nil {
		return fmt.Errorf("missing manifest")
	}
	if _, err := semver.StrictNewVersion(m.Version); err != nil {
		return fmt.Errorf("invalid version: %w", err)
	}
	if !channelPattern.MatchString(m.Channel) {
		return fmt.Errorf("invalid channel")
	}
	if len(expectedChannel) > 0 && m.Channel != expectedChannel[0] {
		return fmt.Errorf("manifest channel mismatch")
	}
	if m.MinVersion != nil {
		min, err := semver.StrictNewVersion(*m.MinVersion)
		if err != nil {
			return fmt.Errorf("invalid min_version: %w", err)
		}
		version, _ := semver.StrictNewVersion(m.Version)
		if min.GreaterThan(version) {
			return fmt.Errorf("min_version exceeds version")
		}
	}
	if m.ZipSize <= 0 || m.ZipSize > int64(maxUnpackedBytes) {
		return fmt.Errorf("invalid zip_size (maximum 8 GiB)")
	}
	hash, err := hex.DecodeString(m.SHA256)
	if err != nil || len(hash) != 32 {
		return fmt.Errorf("invalid sha256")
	}
	return validateUpdateURL(m.ZipURL)
}

func upgradeSameHostArtifactURL(serverURL, artifactURL string) string {
	server, err := url.Parse(serverURL)
	if err != nil || !strings.EqualFold(server.Scheme, "https") {
		return artifactURL
	}
	artifact, err := url.Parse(artifactURL)
	if err != nil || !strings.EqualFold(artifact.Scheme, "http") || artifact.Host == "" {
		return artifactURL
	}
	if !strings.EqualFold(artifact.Hostname(), server.Hostname()) {
		return artifactURL
	}
	artifact.Scheme = "https"
	return artifact.String()
}

func validateUpdateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid update URL: %w", err)
	}
	if u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("invalid update URL authority or fragment")
	}
	if u.Scheme == "https" {
		return nil
	}
	ip := net.ParseIP(u.Hostname())
	if u.Scheme == "http" && (u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())) {
		return nil
	}
	return fmt.Errorf("update URL requires HTTPS (HTTP allowed only for loopback development)")
}

// Manifest is the JSON body returned by GET /api/public/updates/check when an
// update is available. A 204 response means "up to date" and produces no
// manifest. The launcher never trusts the zip without verifying sha256.
type Manifest struct {
	Version    string    `json:"version"`
	Channel    string    `json:"channel"`
	ZipURL     string    `json:"zip_url"`
	ZipSize    int64     `json:"zip_size"`
	SHA256     string    `json:"sha256"`
	Force      bool      `json:"force"`
	MinVersion *string   `json:"min_version,omitempty"`
	Changelog  string    `json:"changelog,omitempty"`
	ReleasedAt time.Time `json:"released_at"`
}
