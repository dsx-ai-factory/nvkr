// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestNativeGuestsUseTenantIPAMPool(t *testing.T) {
	tenant := NewTenantRequest("allocation-contract")
	for _, name := range []string{"nvke-native-site", "nvke-native-guest"} {
		t.Run(name, func(t *testing.T) {
			startup := "#cloud-config\nprivate-handoff: test-only-secret\n"
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != http.MethodPost || r.URL.Path != "/v2/org/"+OrgID+"/forge/instance" ||
					r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Authorization") != "Bearer nico-simctl-token" {
					t.Error("create did not use the pinned NICo request contract")
				}
				var request struct {
					Name       string              `json:"name"`
					Type       string              `json:"instanceTypeId"`
					VPC        string              `json:"vpcId"`
					UserData   string              `json:"userData"`
					PhoneHome  bool                `json:"phoneHomeEnabled"`
					Interfaces []map[string]string `json:"interfaces"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request.Name != name || request.Type != TenantInstanceType || request.VPC != VPCID || request.UserData != startup || !request.PhoneHome {
					t.Error("create lost the selected guest or startup data")
				}
				if len(request.Interfaces) != 1 || len(request.Interfaces[0]) != 1 {
					t.Error("guest requires exactly its shared VPC-prefix allocator")
				} else {
					for _, prefix := range []string{tenant.ControlPlane.NICo.Prefixes[0], tenant.NodePools[0].NICo.Prefixes[0]} {
						if request.Interfaces[0]["vpcPrefixId"] != prefix {
							t.Error("site, control-plane and worker do not share one allocation owner")
						}
					}
				}
				_, _ = w.Write([]byte(`{"id":"created-instance"}`))
			}))
			defer server.Close()
			raw, err := createNativeGuest(t.Context(), server.URL, name, TenantInstanceType, []byte(startup), nil)
			if err != nil || string(raw) != `{"id":"created-instance"}` || calls != 1 {
				t.Fatalf("create calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestNativeGuestCreateRejectsHTTPFailuresAndRedirects(t *testing.T) {
	for _, status := range []int{http.StatusFound, http.StatusBadRequest, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Location", "/must-not-follow")
				w.WriteHeader(status)
				_, _ = w.Write([]byte("test-only-private-error"))
			}))
			defer server.Close()
			raw, err := createNativeGuest(t.Context(), server.URL, "site", TenantInstanceType, []byte("#cloud-config\n"), nil)
			if err == nil || len(raw) != 0 || calls != 1 || strings.Contains(err.Error(), "test-only-private-error") {
				t.Fatalf("failure calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestNativeGuestCreateAdmissionBeforeNetwork(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{true: "canceled", false: "oversize"}[canceled], func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
			defer server.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			startup := []byte(strings.Repeat("x", (1<<20)+1))
			if canceled {
				cancel()
				startup = []byte("#cloud-config\n")
			}
			_, err := createNativeGuest(ctx, server.URL, "site", TenantInstanceType, startup, nil)
			if err == nil || calls != 0 || canceled && !errors.Is(err, context.Canceled) {
				t.Fatalf("admission calls=%d error=%v", calls, err)
			}
		})
	}
}

func TestNativePrimaryAddress(t *testing.T) {
	for _, tc := range []struct {
		name      string
		addresses []string
		valid     bool
	}{
		{"allocated pair", []string{"172.30.0.2", "172.30.0.3"}, true},
		{"upper range", []string{"172.30.0.253", "172.30.0.254"}, true},
		{"empty", nil, false},
		{"old subnet response", []string{"172.30.0.2"}, false},
		{"duplicate", []string{"172.30.0.2", "172.30.0.2"}, false},
		{"gateway", []string{"172.30.0.1", "172.30.0.2"}, false},
		{"broadcast", []string{"172.30.0.254", "172.30.0.255"}, false},
		{"foreign", []string{"192.168.64.2", "172.30.0.3"}, false},
		{"invalid", []string{"not-an-ip", "172.30.0.3"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := nativeInstance{Interfaces: []struct {
				IPAddresses []string `json:"ipAddresses"`
			}{{tc.addresses}}}
			got, err := nativePrimaryAddress(row, linuxNativeBridge())
			if (err == nil) != tc.valid || tc.valid && got != tc.addresses[0] || !tc.valid && got != "" {
				t.Fatalf("primary=%q err=%v", got, err)
			}
		})
	}
}

func TestCanceledNativeGuestCreatesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	out := filepath.Join(t.TempDir(), "attempt")
	err := RunNativeGuest(ctx, NativeGuestConfig{Out: out})
	var coded *nvkrerr.Error
	if !errors.Is(err, context.Canceled) || !errors.As(err, &coded) || coded.Code != nvkrerr.CodePreflightFailed {
		t.Fatalf("canceled guest: %v", err)
	}
	if _, err := os.Lstat(out); !os.IsNotExist(err) {
		t.Fatal("canceled guest created output")
	}
}

func TestMacTCGWithoutAdapterRejectsBeforeArtifactOrOutputWork(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("Mac runtime admission")
	}
	for _, site := range []bool{false, true} {
		t.Run(map[bool]string{false: "guest", true: "site"}[site], func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "must-not-exist")
			var err error
			if site {
				err = RunNative(t.Context(), NativeConfig{Accelerator: "tcg", Image: "/absent/image.qcow2", Out: out})
			} else {
				err = RunNativeGuest(t.Context(), NativeGuestConfig{Accelerator: "tcg", Image: "/absent/image.qcow2", Out: out})
			}
			if err == nil || !strings.Contains(err.Error(), "explicit adapter executable") {
				t.Fatal("TCG reached artifact work without its adapter", err)
			}
			if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
				t.Fatal("TCG adapter refusal created output", statErr)
			}
		})
	}
}

func TestNativeReadinessObservation(t *testing.T) {
	failure := errors.New("read failed")
	joined := &CommandError{ExitCode: 1, Cause: failure}
	for _, tc := range []struct {
		name   string
		errors []error
		calls  int
		want   error
	}{
		{"success", nil, 1, nil},
		{"transient observation failure", []error{joined}, 2, nil},
		{"three failures", []error{joined, joined, joined}, 3, failure},
		{"other exit", []error{&CommandError{ExitCode: 2, Cause: failure}}, 1, failure},
		{"cancellation", []error{errors.Join(joined, context.Canceled)}, 1, context.Canceled},
		{"deadline", []error{errors.Join(joined, context.DeadlineExceeded)}, 1, context.DeadlineExceeded},
		{"unjoined process", []error{errors.Join(joined, ErrUnjoined)}, 1, ErrUnjoined},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			raw, err := nativeReadinessObservation(t.Context(), nil, func(context.Context) ([]byte, error) {
				calls++
				if calls <= len(tc.errors) {
					return nil, tc.errors[calls-1]
				}
				return []byte("observed"), nil
			})
			if calls != tc.calls || !errors.Is(err, tc.want) || tc.want == nil && string(raw) != "observed" {
				t.Fatalf("calls=%d raw=%q err=%v", calls, raw, err)
			}
		})
	}
}

func TestNativeReadinessCancellationDuringRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	_, err := nativeReadinessObservation(ctx, nil, func(context.Context) ([]byte, error) {
		calls++
		cancel()
		return nil, &CommandError{ExitCode: 1, Cause: errors.New("read failed")}
	})
	if calls != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
