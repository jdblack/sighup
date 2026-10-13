package roller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestRegistryDigest exercises the real resolver against a minimal Docker Registry v2: an anonymous
// ping, then a manifest HEAD carrying Docker-Content-Digest. This is the one path the fake digest
// source in every other test deliberately skips.
func TestRegistryDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)

	mux := http.NewServeMux()
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodHead && strings.Contains(r.URL.Path, "/manifests/") {
			w.Header().Set("Docker-Content-Digest", digest)
			w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
			w.Header().Set("Content-Length", "2")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// The httptest host is loopback, which go-containerregistry talks to over plain HTTP.
	ref := strings.TrimPrefix(srv.URL, "http://") + "/library/someapp:edge"

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := Registry{}.Digest(ctx, ref)
	if err != nil {
		t.Fatalf("Digest(%q): %v", ref, err)
	}
	if got != digest {
		t.Fatalf("Digest = %q, want %q", got, digest)
	}
}

func TestRegistryDigestBadRef(t *testing.T) {
	if _, err := (Registry{}).Digest(context.Background(), ""); err == nil {
		t.Fatal("expected an error for an unparseable reference")
	}
}

func TestRegistryDigestMissingManifest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	ref := strings.TrimPrefix(srv.URL, "http://") + "/library/missing:edge"
	if _, err := (Registry{}).Digest(context.Background(), ref); err == nil {
		t.Fatal("expected an error when the manifest is absent")
	}
}
