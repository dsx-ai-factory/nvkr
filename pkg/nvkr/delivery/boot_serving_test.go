// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/readbudget"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestBootServingChecksCompleteExactRawResponse(t *testing.T) {
	const payload = "synthetic raw EFI payload"
	for _, tc := range []struct {
		name         string
		status       int
		body         string
		encoding     string
		rangeHeader  string
		lengthHeader string
		chunked      bool
		bearer       bool
		wantError    bool
		code         e.Code
	}{
		{name: "anonymous", status: 200, body: payload},
		{name: "chunked without length", status: 200, body: payload, chunked: true},
		{name: "incorrect declared length", status: 200, body: payload, lengthHeader: "99", wantError: true},
		{name: "transient unavailable", status: 503, body: "synthetic failure", wantError: true, code: e.CodeUnavailable},
		{name: "independent bearer", status: 200, body: payload, bearer: true},
		{name: "wrong bytes same size", status: 200, body: strings.Repeat("x", len(payload)), wantError: true},
		{name: "truncated", status: 200, body: payload[:10], wantError: true},
		{name: "trailing bytes", status: 200, body: payload + "extra", wantError: true},
		{name: "redirect", status: 302, body: payload, wantError: true},
		{name: "partial", status: 206, body: payload, wantError: true},
		{name: "range despite status", status: 200, body: payload, rangeHeader: "bytes 0-23/24", wantError: true},
		{name: "encoded", status: 200, body: payload, encoding: "gzip", wantError: true},
		{name: "denied", status: 403, body: payload, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			object := Object{SHA256: digestBytes([]byte(payload)), Size: int64(len(payload)), MediaType: MediaOctetStream}
			object.Key = mirrorBlobKey(object.SHA256)
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodGet || r.URL.Path != "/boot/"+object.Key || r.URL.RawQuery != "" {
					t.Error("unexpected client mapping", r.Method, r.URL)
				}
				wantAuth := ""
				if tc.bearer {
					wantAuth = "Bearer synthetic-reader-token"
				}
				if r.Header.Get("Authorization") != wantAuth {
					t.Error("incorrect independent reader credential")
				}
				if r.Header.Get("Accept-Encoding") != "identity" {
					t.Error("reader did not require raw bytes")
				}
				w.Header().Set("Content-Encoding", tc.encoding)
				w.Header().Set("Content-Range", tc.rangeHeader)
				w.Header().Set("Location", "/redirected")
				if tc.lengthHeader != "" {
					w.Header().Set("Content-Length", tc.lengthHeader)
				}
				w.WriteHeader(tc.status)
				if tc.chunked {
					w.(http.Flusher).Flush()
				}
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			originalTransport := http.DefaultTransport
			http.DefaultTransport = server.Client().Transport
			defer func() { http.DefaultTransport = originalTransport }()
			config := definition.BootServing{Profile: definition.BootServingProfile, Origin: server.URL, Prefix: "/boot", ReaderMode: "anonymous"}
			credential := BootReaderCredential{}
			if tc.bearer {
				config.ReaderMode, config.CredentialRef = "bearer", "github:BOOT_READER"
				credential = BootReaderCredential{Ref: config.CredentialRef, Bearer: "synthetic-reader-token"}
			}
			reader, err := newBootServingClient(t.Context(), config, credential, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.close()
			location, err := reader.verify(t.Context(), object)
			if tc.wantError {
				code := tc.code
				if code == "" {
					code = e.CodeVerifyFailed
				}
				requireObjectCode(t, err, code)
			} else if err != nil || location != server.URL+"/boot/"+object.Key {
				t.Fatal("raw response verification", location, err)
			}
			if calls != 1 {
				t.Fatal("redirect or retry escaped exact client request", calls)
			}
			object.Key = "latest.efi"
			_, err = reader.verify(t.Context(), object)
			requireObjectCode(t, err, e.CodeVerifyFailed)
			if calls != 1 {
				t.Fatal("mutable alias reached network")
			}
		})
	}
}

func TestBootServingFailureClassificationRedactsTransportDetails(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code e.Code
	}{
		{name: "DNS", err: &net.DNSError{Err: "synthetic-secret", Name: "sensitive.invalid"}, code: e.CodeUnavailable},
		{name: "timeout", err: &net.DNSError{Err: "synthetic-secret", IsTimeout: true}, code: e.CodeTimeout},
		{name: "truncated read", err: io.ErrUnexpectedEOF, code: e.CodeUnavailable},
		{name: "exhausted allowance", err: e.New(e.CodeEngineCapability, "synthetic-secret"), code: e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := bootServingFailure(t.Context(), tc.err, "boot response failed")
			requireObjectCode(t, err, tc.code)
			if strings.Contains(err.Error(), "synthetic-secret") || strings.Contains(err.Error(), "sensitive.invalid") {
				t.Fatal("underlying transport detail leaked")
			}
		})
	}
	credential := BootReaderCredential{Ref: "github:BOOT_READER", Bearer: "synthetic-secret"}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, credential), credential.Bearer) {
			t.Fatal("credential formatting exposes bearer")
		}
	}
	raw, err := json.Marshal(credential)
	if err != nil || string(raw) != "{}" {
		t.Fatal("credential entered JSON", err)
	}
}

func TestBootServingAdmissionDoesNotInheritWriterAccess(t *testing.T) {
	for _, tc := range []struct {
		name       string
		mode, ref  string
		credential BootReaderCredential
	}{
		{name: "anonymous plus credential", mode: "anonymous", credential: BootReaderCredential{Bearer: "synthetic"}},
		{name: "missing reader", mode: "bearer", ref: "github:BOOT_READER"},
		{name: "writer substituted", mode: "bearer", ref: "github:BOOT_READER", credential: BootReaderCredential{Ref: "github:S3_WRITER", Bearer: "synthetic"}},
		{name: "header injection", mode: "bearer", ref: "github:BOOT_READER", credential: BootReaderCredential{Ref: "github:BOOT_READER", Bearer: "synthetic\r\nother"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := definition.BootServing{Profile: definition.BootServingProfile, Origin: "https://synthetic.invalid", Prefix: "/", ReaderMode: tc.mode, CredentialRef: tc.ref}
			_, err := newBootServingClient(t.Context(), config, tc.credential, time.Second)
			requireObjectCode(t, err, e.CodeTrustPolicyMissing)
		})
	}
}

func TestBootServingReadUsesOperationBudgetAndCancellation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "longer than budget") }))
	defer server.Close()
	originalTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	defer func() { http.DefaultTransport = originalTransport }()
	config := definition.BootServing{Profile: definition.BootServingProfile, Origin: server.URL, Prefix: "/", ReaderMode: "anonymous"}
	ctx, budget, err := readbudget.WithLimit(t.Context(), 4)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := newBootServingClient(ctx, config, BootReaderCredential{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.close()
	object := Object{SHA256: digestBytes([]byte("longer than budget")), Size: 18}
	object.Key = mirrorBlobKey(object.SHA256)
	_, err = reader.verify(ctx, object)
	requireObjectCode(t, err, e.CodeEngineCapability)
	if budget.Used() != 4 {
		t.Fatal("serving read escaped operation budget", budget.Used())
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = reader.verify(ctx, object)
	requireObjectCode(t, err, e.CodeTimeout)
}

func TestBootServingCloseCancelsAndJoinsRead(t *testing.T) {
	entered, canceled := make(chan struct{}), make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "7")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(entered)
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()
	originalTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	defer func() { http.DefaultTransport = originalTransport }()
	config := definition.BootServing{Profile: definition.BootServingProfile, Origin: server.URL, Prefix: "/", ReaderMode: "anonymous"}
	reader, err := newBootServingClient(t.Context(), config, BootReaderCredential{}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.close()
	object := Object{SHA256: digestBytes([]byte("payload")), Size: 7}
	object.Key = mirrorBlobKey(object.SHA256)
	finished := make(chan error, 1)
	go func() { _, err := reader.verify(t.Context(), object); finished <- err }()
	select {
	case <-entered:
	case err := <-finished:
		t.Fatal("read ended before entering server", err)
	}
	reader.close()
	requireObjectCode(t, <-finished, e.CodeTimeout)
	select {
	case <-canceled:
	case <-time.After(5 * time.Second):
		t.Fatal("serving close left server read active")
	}
	_, err = reader.verify(t.Context(), object)
	requireObjectCode(t, err, e.CodeTimeout)
}

func TestBootServingLifetimeDoesNotRestartForAnotherRead(t *testing.T) {
	config := definition.BootServing{Profile: definition.BootServingProfile, Origin: "https://synthetic.invalid", Prefix: "/", ReaderMode: "anonymous"}
	reader, err := newBootServingClient(t.Context(), config, BootReaderCredential{}, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.close()
	<-reader.lifetime.Done()
	object := Object{SHA256: digestBytes([]byte("payload")), Size: 7}
	object.Key = mirrorBlobKey(object.SHA256)
	_, err = reader.verify(t.Context(), object)
	requireObjectCode(t, err, e.CodeTimeout)
}
