package delivery

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func TestPullMetadataProtocolFailuresAreNotRetryable(t *testing.T) {
	for _, test := range []struct {
		name         string
		tlsServer    bool
		wantRequests int64
	}{
		{"HTTP endpoint for HTTPS reference", false, 0},
		{"redirect limit", true, 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			// The default registry transport is process-global. Keep these cases
			// sequential and restore it before other parallel tests can run.
			var connections, requests, insecureRequests atomic.Int64
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.TLS == nil {
					insecureRequests.Add(1)
				}
				http.Redirect(w, r, "/v2/", http.StatusFound)
			}))
			server.Config.ErrorLog = log.New(io.Discard, "", 0)
			roots := x509.NewCertPool()
			if test.tlsServer {
				certificate := metadataRegistryTLSCertificate(t, "registry.example.test", false)
				roots.AddCert(certificate.Leaf)
				server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			original := remote.DefaultTransport
			base, ok := original.(*http.Transport)
			if !ok {
				t.Fatalf("default registry transport has unexpected type %T", original)
			}
			clientTransport := base.Clone()
			clientTransport.Proxy = nil
			clientTransport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
			dialer := &net.Dialer{Timeout: 5 * time.Second}
			clientTransport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				conn, err := dialer.DialContext(ctx, network, server.Listener.Addr().String())
				if err == nil {
					connections.Add(1)
				}
				return conn, err
			}
			remote.DefaultTransport = clientTransport
			defer func() {
				remote.DefaultTransport = original
				clientTransport.CloseIdleConnections()
			}()
			// The logical host forces HTTPS without the registry library's
			// loopback HTTP fallback; all TCP connections remain local.
			reference := "registry.example.test/product/releases@sha256:" + strings.Repeat("a", 64)
			workspace := t.TempDir()
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			got, err := PullMetadata(ctx, reference, filepath.Join(workspace, "metadata"), RegistryOptions{Anonymous: true})
			if connections.Load() == 0 || requests.Load() != test.wantRequests || insecureRequests.Load() != 0 {
				t.Fatalf("protocol condition was not exercised: connections=%d requests=%d insecureRequests=%d, want requests=%d: %v",
					connections.Load(), requests.Load(), insecureRequests.Load(), test.wantRequests, err)
			}
			var requestErr *url.Error
			if !errors.As(err, &requestErr) {
				t.Fatalf("protocol rejection lost its HTTP request cause: %v", err)
			}
			if test.tlsServer {
				if requestErr.Err.Error() != "stopped after 10 redirects" {
					t.Fatalf("redirect loop failed for another reason: %v", requestErr.Err)
				}
			} else if !errors.Is(err, http.ErrSchemeMismatch) {
				t.Fatalf("HTTP endpoint failed for another reason: %v", err)
			}
			if !reflect.DeepEqual(got, Inspection{}) {
				t.Fatalf("protocol failure returned partial metadata: %+v", got)
			}
			entries, readErr := os.ReadDir(workspace)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("protocol failure exposed output or staging: %v, %v", entries, readErr)
			}
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodeFetchFailed || e.ExitCode(err) != 20 || e.Retryable(err) {
				t.Fatalf("protocol rejection must be nonretryable fetch failure/exit20: exit=%d retryable=%t error=%v", e.ExitCode(err), e.Retryable(err), err)
			}
		})
	}
}
