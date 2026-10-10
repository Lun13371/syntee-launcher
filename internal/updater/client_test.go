package updater

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDownloadRedirectPolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/artifact", http.StatusFound)
	}))
	defer server.Close()
	err := (&Client{}).Download(context.Background(), server.URL, &strings.Builder{}, nil)
	if err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("unsafe redirect accepted: %v", err)
	}
}

func TestTransportTimeouts(t *testing.T) {
	client := updateHTTPClient(2 * time.Minute)
	defer client.CloseIdleConnections()
	transport := client.Transport.(*http.Transport)
	if client.Timeout != 2*time.Minute || transport.TLSHandshakeTimeout <= 0 || transport.ResponseHeaderTimeout <= 0 || transport.DialContext == nil || !transport.DisableCompression {
		t.Fatal("missing transport limits")
	}
}
