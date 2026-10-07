package lifecycle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestInspectReleaseRegistryFailureClassification(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    int
		code      e.Code
		exit      int
		retryable bool
	}{
		{"service unavailable", http.StatusServiceUnavailable, e.CodeUnavailable, 30, true},
		{"request timeout", http.StatusRequestTimeout, e.CodeUnavailable, 30, true},
		{"rate limited", http.StatusTooManyRequests, e.CodeUnavailable, 30, true},
		{"forbidden", http.StatusForbidden, e.CodeFetchFailed, 20, false},
		{"not found", http.StatusNotFound, e.CodeFetchFailed, 20, false},
		{"deadline", 0, e.CodeTimeout, 31, true},
		{"canceled during request", 0, e.CodeTimeout, 31, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/" {
					w.WriteHeader(http.StatusOK)
					return
				}
				requests.Add(1)
				if test.status != 0 {
					http.Error(w, "registry inspection rejected", test.status)
					return
				}
				if test.name == "canceled during request" {
					cancel()
				}
				<-r.Context().Done()
			}))
			defer server.Close()
			workspace := t.TempDir()
			options := InspectionOptions{WorkspaceRoot: workspace, Timeout: 30 * time.Second,
				Registry: delivery.RegistryOptions{Anonymous: true, PlainHTTP: true}}
			if test.name == "deadline" {
				options.Timeout = 250 * time.Millisecond
			}
			reference := strings.TrimPrefix(server.URL, "http://") + "/product/releases@sha256:" + strings.Repeat("a", 64)
			view, err := InspectRelease(ctx, reference, options)
			assertCode(t, err, test.code)
			if e.Retryable(err) != test.retryable || e.ExitCode(err) != test.exit {
				t.Fatalf("inspection changed public retry/exit classification: retryable=%t exit=%d error=%v", e.Retryable(err), e.ExitCode(err), err)
			}
			// The deadline may expire before the request starts on a loaded
			// runner; the cancellation case deterministically exercises in-flight I/O.
			if test.name != "deadline" && requests.Load() == 0 {
				t.Fatal("failure did not exercise metadata registry acquisition")
			}
			if !reflect.DeepEqual(view, delivery.PublicationDescription{}) {
				t.Fatalf("registry failure returned a partial view: %+v", view)
			}
			entries, err := os.ReadDir(workspace)
			if err != nil || len(entries) != 0 {
				t.Fatalf("registry failure retained inspection staging: %v, %v", entries, err)
			}
		})
	}
}
