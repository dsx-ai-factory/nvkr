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
	"time"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestNativeSimulatorReadinessBudget(t *testing.T) {
	for _, tc := range []struct {
		name    string
		site    bool
		timeout time.Duration
		budget  time.Duration
		parent  time.Duration
	}{
		{name: "site inherits work deadline", site: true, budget: 2 * time.Hour},
		{name: "site ignores standalone and tenant limits", site: true, timeout: time.Minute, budget: 30 * time.Minute},
		{name: "site respects earlier parent", site: true, budget: 2 * time.Hour, parent: 15 * time.Minute},
		{name: "standalone explicit budget", timeout: 5 * time.Minute},
		{name: "standalone earlier caller", timeout: 5 * time.Minute, budget: time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			for _, budget := range []time.Duration{tc.parent, tc.budget} {
				if budget != 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, budget)
					defer cancel()
				}
			}
			cfg := NativeGuestConfig{Timeout: tc.timeout, Accelerator: "kvm"}
			if tc.site {
				cfg.site = &nativeSiteAttempt{tenantTimeout: 2 * time.Hour}
			}
			deadline, _ := ctx.Deadline()
			before := time.Until(deadline)
			values, err := nativeSimulatorEnvironment(ctx, cfg, linuxNativeBridge(), "/image.qcow2", "/seed.yaml", "/simulator")
			after := time.Until(deadline)
			if err != nil {
				t.Fatal(err)
			}
			env := SetEnv([]string{"NICO_VMS_READINESS_TIMEOUT=0s"}, values)
			got, err := time.ParseDuration(environmentValue(env, "NICO_VMS_READINESS_TIMEOUT"))
			if err != nil || got <= 0 || tc.site && (got < after || got > before) || !tc.site && got != tc.timeout {
				t.Fatalf("simulator readiness timeout=%v err=%v; caller remaining=[%v,%v]", got, err, after, before)
			}
			if strings.Count(strings.Join(env, "\n"), "NICO_VMS_READINESS_TIMEOUT=") != 1 {
				t.Fatal("simulator environment retained a stale readiness timeout")
			}
			for key, want := range map[string]string{"NICO_VMS_CLEANUP_ORPHANED_VMS": "false", "NICO_VMS_DIAG_SSH_ENABLED": "false", "NICO_VMS_BASE_IMAGE": "/image.qcow2"} {
				if got := environmentValue(env, key); got != want {
					t.Fatalf("%s=%q, want %q", key, got, want)
				}
			}
			if cfg.Timeout != tc.timeout {
				t.Fatal("simulator budget changed the guest readiness policy")
			}
		})
	}
}

func TestNativeSimulatorRejectsUnavailableSiteBudget(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer cancel()
	for _, tc := range []struct {
		name  string
		ctx   context.Context
		code  nvkrerr.Code
		cause error
	}{
		{"nil", nil, nvkrerr.CodeUsage, nil},
		{"unbounded", context.Background(), nvkrerr.CodeUsage, nil},
		{"canceled", canceled, nvkrerr.CodeTimeout, context.Canceled},
		{"expired", expired, nvkrerr.CodeTimeout, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, err := nativeSimulatorEnvironment(tc.ctx, NativeGuestConfig{site: &nativeSiteAttempt{}}, linuxNativeBridge(), "/image.qcow2", "/seed.yaml", "/simulator")
			var coded *nvkrerr.Error
			if env != nil || !errors.As(err, &coded) || coded.Code != tc.code || tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("environment=%v error=%v", env, err)
			}
		})
	}
}

func TestNativeGuestsUseTenantIPAMPool(t *testing.T) {
	attempt := nativeCAPITenantFixture(t, GuestCIDR, "ovn")
	tenant, err := attempt.namedTenantRequest(t.Context(), "allocation-contract")
	if err != nil {
		t.Fatal(err)
	}
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
					for _, infra := range []*TenantMachineInfra{tenant.ControlPlane.Infra, tenant.NodePools[0].Infra} {
						var machine struct {
							Spec struct {
								Template struct {
									Spec struct {
										Type       string              `json:"instanceTypeID"`
										VPC        string              `json:"vpcID"`
										Interfaces []map[string]string `json:"interfaces"`
									} `json:"spec"`
								} `json:"template"`
							} `json:"spec"`
						}
						if err := json.Unmarshal(infra.MachineTemplate, &machine); err != nil {
							t.Fatal(err)
						}
						instance := machine.Spec.Template.Spec
						if instance.Type != request.Type || instance.VPC != request.VPC || len(instance.Interfaces) != 1 || request.Interfaces[0]["vpcPrefixId"] != instance.Interfaces[0]["vpcPrefixID"] {
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
	queryTimeout := &CommandError{ExitCode: 143, Cause: errors.Join(context.DeadlineExceeded, errCommandDeadline)}
	for _, tc := range []struct {
		name   string
		errors []error
		calls  int
		want   error
	}{
		{"success", nil, 1, nil},
		{"transient observation failure", []error{joined}, 2, nil},
		{"recovers after four failures", []error{joined, joined, joined, joined}, 5, nil},
		{"simulator exit is terminal", []error{errSimulatorExited}, 1, errSimulatorExited},
		{"simulator exit cannot be hidden by exit one", []error{errors.Join(errSimulatorExited, joined)}, 1, errSimulatorExited},
		{"other exit", []error{&CommandError{ExitCode: 2, Cause: failure}}, 1, failure},
		{"cancellation", []error{errors.Join(joined, context.Canceled)}, 1, context.Canceled},
		{"deadline", []error{errors.Join(joined, context.DeadlineExceeded)}, 1, context.DeadlineExceeded},
		{"joined query deadline", []error{queryTimeout}, 2, nil},
		{"query deadline with unjoined process", []error{errors.Join(queryTimeout, ErrUnjoined)}, 1, ErrUnjoined},
		{"query deadline with cancellation", []error{errors.Join(queryTimeout, context.Canceled)}, 1, context.Canceled},
		{"query deadline with simulator exit", []error{errors.Join(queryTimeout, errSimulatorExited)}, 1, errSimulatorExited},
		{"unjoined process", []error{errors.Join(joined, ErrUnjoined)}, 1, ErrUnjoined},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			calls := 0
			raw, err := nativeReadinessObservation(ctx, nil, func(context.Context) ([]byte, error) {
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

func TestNativeReadinessRecoversAfterJoinedCommandDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	deadline, _ := ctx.Deadline()
	runner := Runner{Work: privateTestDir(t), Env: []string{"PATH=/usr/bin:/bin"}}
	calls := 0
	raw, err := nativeReadinessObservation(ctx, nil, func(ctx context.Context) ([]byte, error) {
		calls++
		if got, _ := ctx.Deadline(); got != deadline {
			t.Fatal("observation replaced the original deployment deadline")
		}
		if calls == 1 {
			partial, err := runner.Capture(ctx, []string{"/bin/sh", "-c", "printf incomplete; exec sleep 60"}, "read-timeout", 100*time.Millisecond)
			if !errors.Is(err, errCommandDeadline) || !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrUnjoined) {
				t.Fatalf("query did not return a joined independent timeout: %v", err)
			}
			return partial, err
		}
		return runner.Capture(ctx, []string{"/bin/sh", "-c", "printf observed"}, "read-success", time.Second)
	})
	if calls != 2 || err != nil || string(raw) != "observed" {
		t.Fatalf("calls=%d raw=%q err=%v", calls, raw, err)
	}
}

func TestNativeReadinessCancellationDuringRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
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

func TestNativeReadinessRequiresOriginalBudget(t *testing.T) {
	canceled, cancel := context.WithTimeout(t.Context(), time.Minute)
	cancel()
	expired, stopExpired := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer stopExpired()
	for _, tc := range []struct {
		name string
		ctx  context.Context
		code nvkrerr.Code
		want error
	}{
		{"nil", nil, nvkrerr.CodeUsage, nil},
		{"unbounded", context.Background(), nvkrerr.CodeUsage, nil},
		{"canceled before observation", canceled, nvkrerr.CodeTimeout, context.Canceled},
		{"expired before observation", expired, nvkrerr.CodeTimeout, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			_, err := nativeReadinessObservation(tc.ctx, nil, func(context.Context) ([]byte, error) {
				calls++
				return []byte("must not run"), nil
			})
			var coded *nvkrerr.Error
			if calls != 0 || !errors.As(err, &coded) || coded.Code != tc.code || tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestNativeReadinessStopsAtOriginalDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	failure := &CommandError{ExitCode: 1, Cause: errors.New("read failed")}
	calls := 0
	raw, err := nativeReadinessObservation(ctx, nil, func(context.Context) ([]byte, error) {
		calls++
		return nil, failure
	})
	if calls != 1 || len(raw) != 0 || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, failure) || CommandExitCode(err) != -1 {
		t.Fatalf("calls=%d raw=%q err=%v", calls, raw, err)
	}
}

func TestNativeReadinessRejectsLateSuccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	calls := 0
	raw, err := nativeReadinessObservation(ctx, nil, func(context.Context) ([]byte, error) {
		calls++
		cancel()
		return []byte("too late"), nil
	})
	if calls != 1 || len(raw) != 0 || !errors.Is(err, context.Canceled) {
		t.Fatalf("calls=%d raw=%q err=%v", calls, raw, err)
	}
}
