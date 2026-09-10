package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/pipeline"
)

func TestActionsBumpCommandCommitsOnlySelectedResolvedPins(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		check, current, track bool
		apiFailure            bool
		code                  e.Code
	}{
		{name: "update selected pin"},
		{name: "current pins", current: true},
		{name: "check stale", check: true, code: e.CodePipelineDrift},
		{name: "retarget and update", track: true},
		{name: "retarget same commit", track: true, current: true},
		{name: "check retarget", check: true, track: true, current: true, code: e.CodePipelineDrift},
		{name: "API failure preserves file", apiFailure: true, code: e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lock := pipeline.EmbeddedActionLock()
			before, err := lock.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "actions.lock.yaml")
			if err := os.WriteFile(path, before, 0o644); err != nil {
				t.Fatal(err)
			}
			pin := lock.Actions["checkout"]
			sha, track := strings.Repeat("a", 40), pin.Track
			if tc.current {
				sha = pin.SHA
			}
			if tc.track {
				track = "v99"
			}
			version := track + ".1.2"
			original := http.DefaultTransport
			requests := 0
			http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				requests++
				if r.URL.Host != "api.github.com" || r.Header.Get("Authorization") != "Bearer fixture-token" || r.Method != http.MethodGet {
					return nil, fmt.Errorf("unexpected API request: %s %s", r.Method, r.URL)
				}
				status, body := http.StatusOK, ""
				switch r.URL.Path {
				case "/repos/" + pin.Repo + "/commits/" + track:
					body = `{"sha":"` + sha + `"}`
				case "/repos/" + pin.Repo + "/tags":
					body = `[{"name":"` + version + `","commit":{"sha":"` + sha + `"}}]`
				default:
					return nil, fmt.Errorf("unselected action request: %s", r.URL)
				}
				if tc.apiFailure {
					status, body = http.StatusForbidden, "denied"
				}
				return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = original })
			args := []string{"actions", "bump", "--lockfile", path, "--only", "checkout", "--token", "fixture-token"}
			if tc.check {
				args = append(args, "--check")
			}
			if tc.track {
				args = append(args, "--track", "checkout="+track)
			}
			output, err := invokeLifecycleCLI(t, args...)
			if tc.code != "" {
				assertLifecycleCommandCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if tc.check || tc.apiFailure || (tc.current && !tc.track) {
				if !bytes.Equal(before, after) {
					t.Fatal("non-updating command changed lock bytes")
				}
			} else {
				got, err := pipeline.ParseActionLock(after)
				if err != nil {
					t.Fatal(err)
				}
				pin.SHA, pin.Track, pin.Version = sha, track, version
				lock.Actions["checkout"] = pin
				if !reflect.DeepEqual(got, lock) || !strings.Contains(output, "updated ") {
					t.Fatalf("unexpected lock update: %+v; output %q", got, output)
				}
			}
			wantRequests := 2
			if tc.apiFailure {
				wantRequests = 1
			}
			if requests != wantRequests {
				t.Fatalf("API calls = %d; want %d", requests, wantRequests)
			}
		})
	}
}
