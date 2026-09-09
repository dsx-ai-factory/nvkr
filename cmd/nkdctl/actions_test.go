package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGitHubRefResolverAgainstFakeAPI exercises the real resolver end-to-end against a stand-in
// GitHub API: track -> commit sha, and the precise vX.Y.Z release label at that sha.
func TestGitHubRefResolverAgainstFakeAPI(t *testing.T) {
	const sha = "40f1582b2485089dde7abd97c1529aa768e1baff"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/actions/setup-go/commits/v5":
			_, _ = w.Write([]byte(`{"sha":"` + sha + `"}`))
		case "/repos/actions/setup-go/tags":
			// v5 (moving) and v5.6.0 (precise) both point at the sha; the resolver must pick v5.6.0.
			_, _ = w.Write([]byte(`[{"name":"v5","commit":{"sha":"` + sha + `"}},{"name":"v5.6.0","commit":{"sha":"` + sha + `"}}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	r := newGitHubRefResolver("")
	r.baseURL = srv.URL
	gotSHA, gotVersion, err := r.Resolve(context.Background(), "actions/setup-go", "v5")
	if err != nil {
		t.Fatal(err)
	}
	if gotSHA != sha {
		t.Fatalf("sha = %q, want %q", gotSHA, sha)
	}
	if gotVersion != "v5.6.0" {
		t.Fatalf("version = %q, want v5.6.0", gotVersion)
	}
}

func TestGitHubRefResolverSurfacesAPIErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	defer srv.Close()
	r := newGitHubRefResolver("")
	r.baseURL = srv.URL
	if _, _, err := r.Resolve(context.Background(), "actions/nope", "v1"); err == nil {
		t.Fatal("a 404 from the API must surface as an error, not a silent empty pin")
	}
}
