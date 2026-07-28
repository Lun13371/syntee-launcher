// Package updater handles checking the gateway for new client releases,
// downloading the zip artifact (with HTTP Range resume), verifying its SHA256
// and applying it atomically to current/.
package updater

import "time"

// Manifest is the JSON body returned by GET /api/public/updates/check when an
// update is available. A 204 response means "up to date" and produces no
// manifest. The launcher never trusts the zip without verifying sha256.
type Manifest struct {
	Version     string    `json:"version"`
	Channel     string    `json:"channel"`
	ZipURL      string    `json:"zip_url"`
	ZipSize     int64     `json:"zip_size"`
	SHA256      string    `json:"sha256"`
	Force       bool      `json:"force"`
	MinVersion  *string   `json:"min_version,omitempty"`
	Changelog   string    `json:"changelog,omitempty"`
	ReleasedAt  time.Time `json:"released_at"`
}
