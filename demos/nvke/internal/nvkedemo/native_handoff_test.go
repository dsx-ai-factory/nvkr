// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
)

func TestNativeHandoffSeparatesPreparationFromCredentials(t *testing.T) {
	for _, tc := range []struct {
		status, file string
		code         int
		failed       bool
	}{
		{"failed", "site-startup-failure.log", http.StatusAccepted, true},
		{"prepared", "site-prepared.json", http.StatusCreated, false},
	} {
		t.Run(tc.status, func(t *testing.T) {
			work := t.TempDir()
			if err := os.Chmod(work, 0700); err != nil {
				t.Fatal(err)
			}
			root, _, err := securepath.OpenDirectory(work)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = root.Close() })
			h := &nativeHandoff{root: root, Token: "fixture-token", result: make(chan error, 1)}
			r := httptest.NewRequest(http.MethodPost, "/site-kubeconfig", strings.NewReader("private startup result"))
			r.Header.Set("Authorization", "Bearer "+h.Token)
			r.Header.Set("X-NVKE-Startup-Status", tc.status)
			w := httptest.NewRecorder()
			h.receive(w, r)
			if w.Code != tc.code {
				t.Fatalf("handoff status=%d", w.Code)
			}
			if err := h.Wait(t.Context()); (err != nil) != tc.failed {
				t.Fatalf("handoff outcome: %v", err)
			}
			if _, err := os.Stat(filepath.Join(work, "site.kubeconfig")); !os.IsNotExist(err) {
				t.Fatal("preparation or failure became a kubeconfig")
			}
			info, err := os.Stat(filepath.Join(work, tc.file))
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("handoff result is not private", err)
			}
		})
	}
}

func TestNativeHandoffAuthenticatesBoundsAndPreservesSourceBytes(t *testing.T) {
	work := t.TempDir()
	if err := os.Chmod(work, 0o700); err != nil {
		t.Fatal(err)
	}
	handoff, err := startNativeHandoff(t.Context(), "127.0.0.1", work)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := handoff.Stop(ctx); err != nil {
			t.Error(err)
		}
	})
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(handoff.Certificate)) {
		t.Fatal("server did not issue its private certificate")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	send := func(body, token string) int {
		t.Helper()
		request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, handoff.URL, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		return response.StatusCode
	}
	for _, tc := range []struct {
		name, body, token string
		want              int
	}{
		{"unauthorized", "secret", "wrong-token", http.StatusUnauthorized},
		{"empty", "", handoff.Token, http.StatusBadRequest},
		{"oversized", strings.Repeat("x", (1<<20)+1), handoff.Token, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := send(tc.body, tc.token); got != tc.want {
				t.Fatalf("HTTP status=%d want=%d", got, tc.want)
			}
			if _, err := os.Stat(filepath.Join(work, "site.kubeconfig")); !os.IsNotExist(err) {
				t.Fatal("rejected upload wrote credential bytes")
			}
		})
	}
	const original = "apiVersion: v1\nclusters:\n- cluster:\n    server: https://172.30.0.10:6443\n"
	if got := send(original, handoff.Token); got != http.StatusCreated {
		t.Fatalf("accepted handoff HTTP status=%d", got)
	}
	if err := handoff.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(work, "site.kubeconfig"))
	if err != nil || string(raw) != original {
		t.Fatal("handoff changed the source's endpoint or credential bytes")
	}
	info, err := os.Stat(filepath.Join(work, "site.kubeconfig"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("handoff exposed credential bytes")
	}
	if got := send("replacement", handoff.Token); got != http.StatusConflict {
		t.Fatalf("duplicate handoff HTTP status=%d", got)
	}
}

func TestNativeHandoffRejectsPublicBindAndPublicWorkspace(t *testing.T) {
	for _, tc := range []struct {
		name, bind string
		mode       os.FileMode
	}{
		{"public bind", "8.8.8.8", 0o700},
		{"public workspace", "127.0.0.1", 0o755},
	} {
		t.Run(tc.name, func(t *testing.T) {
			work := t.TempDir()
			if err := os.Chmod(work, tc.mode); err != nil {
				t.Fatal(err)
			}
			handoff, err := startNativeHandoff(t.Context(), tc.bind, work)
			if err == nil || handoff != nil {
				t.Fatal("inadmissible handoff started a listener")
			}
		})
	}
}
