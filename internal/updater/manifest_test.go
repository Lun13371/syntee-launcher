package updater

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManifestValidation(t *testing.T) {
	good := testManifest("https://downloads.example.com/client.zip", "abc")
	tests := []struct {
		name   string
		mutate func(*Manifest)
	}{
		{"version", func(m *Manifest) { m.Version = "1.2" }},
		{"channel", func(m *Manifest) { m.Channel = "../stable" }},
		{"zero size", func(m *Manifest) { m.ZipSize = 0 }},
		{"hash", func(m *Manifest) { m.SHA256 = strings.Repeat("z", 64) }},
		{"short hash", func(m *Manifest) { m.SHA256 = "abcd" }},
		{"http production", func(m *Manifest) { m.ZipURL = "http://downloads.example.com/file" }},
		{"file URL", func(m *Manifest) { m.ZipURL = "file:///tmp/file" }},
		{"credentials", func(m *Manifest) { m.ZipURL = "https://user:pass@example.com/file" }},
		{"fragment", func(m *Manifest) { m.ZipURL += "#fragment" }},
		{"min version", func(m *Manifest) { value := "bad"; m.MinVersion = &value }},
		{"min newer", func(m *Manifest) { value := "9.0.0"; m.MinVersion = &value }},
	}
	if err := good.Validate("stable"); err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := *good
			tt.mutate(&m)
			if err := m.Validate(); err == nil {
				t.Fatal("accepted invalid manifest")
			}
		})
	}
	if err := good.Validate("beta"); err == nil {
		t.Fatal("accepted wrong channel")
	}
	var missing *Manifest
	if err := missing.Validate(); err == nil {
		t.Fatal("accepted nil")
	}
	for _, url := range []string{"http://localhost:8080/file", "http://127.0.0.1/file", "http://[::1]/file", "https://example.com/file"} {
		m := *good
		m.ZipURL = url
		if err := m.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUpgradeSameHostArtifactURL(t *testing.T) {
	tests := []struct {
		server, artifact, want string
	}{
		{"https://syntee.fun/api/public/updates", "http://syntee.fun/api/public/updates/download/stable/1.1.4", "https://syntee.fun/api/public/updates/download/stable/1.1.4"},
		{"https://syntee.fun/api/public/updates", "http://cdn.syntee.fun/client.zip", "http://cdn.syntee.fun/client.zip"},
		{"http://localhost:8080/api", "http://localhost:8080/file", "http://localhost:8080/file"},
		{"https://syntee.fun/api", "https://syntee.fun/file", "https://syntee.fun/file"},
	}
	for _, tt := range tests {
		if got := upgradeSameHostArtifactURL(tt.server, tt.artifact); got != tt.want {
			t.Errorf("upgradeSameHostArtifactURL(%q, %q) = %q, want %q", tt.server, tt.artifact, got, tt.want)
		}
	}
}

func TestCheckValidatesAndEscapesQuery(t *testing.T) {
	for _, mode := range []string{"good", "wrong channel", "bad hash", "trailing"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("current") != "1.0.0+build&x=1" || r.URL.Query().Get("x") != "" {
					t.Errorf("query not escaped: %s", r.URL.RawQuery)
				}
				m := testManifest("https://example.com/file", "abc")
				if mode == "wrong channel" {
					m.Channel = "beta"
				}
				if mode == "bad hash" {
					m.SHA256 = "bad"
				}
				_ = json.NewEncoder(w).Encode(m)
				if mode == "trailing" {
					_, _ = w.Write([]byte("{}"))
				}
			}))
			defer server.Close()
			c := &Client{http: updateHTTPClient(0), serverURL: server.URL}
			_, err := c.Check(context.Background(), "stable", "1.0.0+build&x=1")
			if (mode == "good") != (err == nil) {
				t.Fatalf("%s: %v", mode, err)
			}
		})
	}
}
