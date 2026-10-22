// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/nvkedemo"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type failedProgressWriter struct{ short bool }

func TestRetainedInstructionsSurviveFailedTerminalExit(t *testing.T) {
	const instructions = "SESSION='/private/fixture'\nDEMO='/private/demo'\n\"$DEMO\" status --session \"$SESSION\"\n\"$DEMO\" cleanup --session \"$SESSION\"\n"
	for _, success := range []bool{false, true} {
		t.Run(fmt.Sprint(success), func(t *testing.T) {
			var out bytes.Buffer
			writer := &checkedWriter{out: &out, retainedInstructions: instructions}
			writer.retainResult("PASS (retained): synthetic successful observation")
			writer.replayResults(success)
			if !strings.Contains(out.String(), instructions) || strings.Contains(out.String(), "PASS (retained):") != success {
				t.Fatal("terminal exit lost recovery commands or invented success", out.String())
			}
		})
	}
}

func TestRetainedInstructionsRejectUnissuedPresentation(t *testing.T) {
	var out bytes.Buffer
	writer := &checkedWriter{out: &out, cancel: func() {}}
	if err := writer.WriteRetainedInstructions(nvkedemo.RetainedInstructions{}); err == nil {
		t.Fatal("unissued instructions crossed presentation boundary")
	}
}

func TestBuildRunLocatorsSurviveConciseOutputAndReplay(t *testing.T) {
	const locators = "Artifacts Run: /workspace/.nvkr/runs/artifacts-001\nNative tools Run: /workspace/.nvkr/native-tools/runs/tools-001\n"
	for _, mode := range []string{"concise", "plain", "dashboard replay", "failed dashboard replay"} {
		t.Run(mode, func(t *testing.T) {
			var output bytes.Buffer
			if strings.Contains(mode, "replay") {
				writer := &checkedWriter{out: &output, started: time.Now()}
				if _, err := io.WriteString(writer, locators); err != nil {
					t.Fatal(err)
				}
				output.Reset()
				writer.replayResults(mode == "dashboard replay")
			} else {
				err := runProgress(t.Context(), &output, mode == "plain", func(_ context.Context, writer io.Writer) error {
					_, err := io.WriteString(writer, locators)
					return err
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, line := range strings.Split(strings.TrimSpace(locators), "\n") {
				if strings.Contains(output.String(), line) == (mode == "failed dashboard replay") {
					t.Fatalf("Run locator visibility differs for %s: %q", mode, output.String())
				}
			}
		})
	}
}

func (w failedProgressWriter) Write(data []byte) (int, error) {
	if w.short {
		return len(data) / 2, nil
	}
	return 0, errors.New("private writer details")
}

func TestProgressFailureCancelsAndWaitsForWork(t *testing.T) {
	for _, short := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "short"}[short], func(t *testing.T) {
			joined := false
			err := runProgress(t.Context(), failedProgressWriter{short: short}, false, func(ctx context.Context, out io.Writer) error {
				out.(nvkedemo.ProgressReporter).Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "build"})
				if ctx.Err() == nil {
					t.Fatal("output failure did not cancel work")
				}
				joined = true
				return nil
			})
			assertCode(t, err, nvkrerr.CodePreflightFailed)
			if !joined || strings.Contains(err.Error(), "private writer") {
				t.Fatal("work unjoined or private error exposed")
			}
		})
	}
}

func TestProgressKeepsObservedBoundariesSeparateFromLogs(t *testing.T) {
	var out bytes.Buffer
	err := runProgress(t.Context(), &out, false, func(_ context.Context, writer io.Writer) error {
		reporter := writer.(nvkedemo.ProgressReporter)
		reporter.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneStarted, Name: "os"})
		_, _ = io.WriteString(writer, "packer output\n")
		reporter.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFailed, Name: "boot\x1b[2J", Status: "secret"})
		reporter.Report(nvkedemo.ProgressEvent{Kind: "unknown", Name: "not observed"})
		return nvkrerr.New(nvkrerr.CodeBuildFailed, "build failed")
	})
	assertCode(t, err, nvkrerr.CodeBuildFailed)
	if !strings.HasPrefix(out.String(), "[START 0s] os\n[FAIL 0s] boot\\x1b[2J\n") || !strings.Contains(out.String(), "Stopped at: boot\\x1b[2J") {
		t.Fatalf("unexpected progress: %q", out.String())
	}
}

func TestProgressConciseAndVerbosePublicOutput(t *testing.T) {
	for _, verbose := range []bool{false, true} {
		t.Run(map[bool]string{false: "concise", true: "verbose"}[verbose], func(t *testing.T) {
			var out, raw bytes.Buffer
			input := "raw compiler noise\n[dry-run] image (build)\n[start] image (build)\nBuild graph: 29 outputs\n  output: image [oci]\n[start] escaped\x1b[2J\n==> qemu.node: Waiting for SSH to become available...\r\n[done] image\nPASS: actual owner result"
			err := progressOutput(t.Context(), &out, &raw, verbose, "", func(_ context.Context, writer io.Writer) error {
				for _, b := range []byte(input) {
					if _, err := writer.Write([]byte{b}); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil || raw.String() != input {
				t.Fatalf("error=%v raw=%q", err, &raw)
			}
			if verbose {
				if out.String() != input {
					t.Fatalf("verbose output changed: %q", &out)
				}
			} else {
				for _, marker := range []string{"[log +", "[dry-run] image", "[start] image", "Build graph:", "  output:", "[done] image", "Waiting for SSH", "escaped\\x1b[2J", "PASS:"} {
					if !strings.Contains(out.String(), marker) {
						t.Fatalf("missing %q: %q", marker, &out)
					}
				}
				if strings.Contains(out.String(), "compiler noise") {
					t.Fatal("concise mode streamed raw noise")
				}
				if strings.ContainsRune(out.String(), '\x1b') {
					t.Fatal("raw terminal control in concise output")
				}
			}
		})
	}
}

func TestProgressBoundsAndLogFailure(t *testing.T) {
	for _, kind := range []string{"huge-line", "log-error", "log-short", "verbose-log-error", "verbose-log-short"} {
		t.Run(kind, func(t *testing.T) {
			var out, raw bytes.Buffer
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			w := &checkedWriter{out: &out, raw: &raw, remaining: 8 << 20, cancel: cancel, started: time.Now()}
			if kind != "huge-line" {
				w.raw = failedProgressWriter{short: strings.HasSuffix(kind, "short")}
				w.verbose = strings.HasPrefix(kind, "verbose")
			}
			data := "[start] " + strings.Repeat("x", 32<<10) + "\n[done] next\n"
			_, err := io.WriteString(w, data)
			if kind == "huge-line" {
				if err != nil || len(w.line) != 0 || strings.Contains(out.String(), "xxxx") || !strings.Contains(out.String(), "[done] next") {
					t.Fatalf("error=%v output=%q", err, &out)
				}
			} else if err == nil || ctx.Err() == nil {
				t.Fatal("log failure did not cancel work")
			} else {
				assertCode(t, err, nvkrerr.CodePreflightFailed)
				if strings.Contains(err.Error(), "private writer") {
					t.Fatal("private log writer error exposed")
				}
			}
		})
	}
}

func TestProgressLogRetentionBound(t *testing.T) {
	const limit = 8 << 20
	const notice = "Build log retention reached 8 MiB; further output is not retained.\n"
	const marker = "[done] after retention limit\n"
	prefix := strings.Repeat("x", limit-1) + "\n"
	for _, tc := range []struct {
		name      string
		chunks    []string
		truncated bool
	}{
		{"exact limit", []string{prefix}, false},
		{"one chunk crosses limit", []string{prefix + marker}, true},
		{"later chunk crosses limit", []string{prefix[:limit-2], prefix[limit-2:] + marker}, true},
		{"writes after limit", []string{prefix, marker, marker}, true},
	} {
		for _, verbose := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/concise", true: "/verbose"}[verbose], func(t *testing.T) {
				var out, raw bytes.Buffer
				err := progressOutput(t.Context(), &out, &raw, verbose, "", func(ctx context.Context, writer io.Writer) error {
					for _, chunk := range tc.chunks {
						n, err := io.WriteString(writer, chunk)
						if err != nil || n != len(chunk) || ctx.Err() != nil {
							t.Fatalf("retention canceled healthy work: n=%d error=%v context=%v", n, err, ctx.Err())
						}
					}
					if verbose && strings.Replace(out.String(), notice, "", 1) != strings.Join(tc.chunks, "") {
						t.Fatal("retention limit changed verbose streaming")
					}
					writer.(nvkedemo.ProgressReporter).Report(nvkedemo.ProgressEvent{Kind: nvkedemo.LaneFinished, Name: "build"})
					return nil
				})
				if err != nil || raw.String() != prefix {
					t.Fatalf("retained prefix differs: length=%d error=%v", raw.Len(), err)
				}
				wantNotices := 0
				if tc.truncated {
					wantNotices = 1
				}
				if strings.Count(out.String(), notice) != wantNotices || !strings.Contains(out.String(), "[DONE ") || !strings.Contains(out.String(), "build") {
					t.Fatal("retention notice count or observed progress differs")
				}
				if tc.truncated && !strings.Contains(out.String(), strings.TrimSuffix(marker, "\n")) {
					t.Fatal("post-limit build marker was lost")
				}
			})
		}
	}
}

func TestBuildLogRetainedPrivatelyOnFailure(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	var out bytes.Buffer
	err := runBuildProgress(t.Context(), &out, false, false, func(_ context.Context, writer io.Writer) error {
		_, err := io.WriteString(writer, "complete public build bytes\n")
		return errors.Join(err, nvkrerr.New(nvkrerr.CodeBuildFailed, "failed producer"))
	})
	assertCode(t, err, nvkrerr.CodeBuildFailed)
	paths, err := filepath.Glob(filepath.Join(os.Getenv("TMPDIR"), "nvke-demo-build-*.log"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("logs=%v error=%v", paths, err)
	}
	info, err := os.Stat(paths[0])
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private log: %v", err)
	}
	data, err := os.ReadFile(paths[0])
	if err != nil || string(data) != "complete public build bytes\n" || !strings.Contains(out.String(), paths[0]) {
		t.Fatalf("log=%q error=%v", data, err)
	}
}

func TestTenantStatusShowsMeaningNotPrivateProbeNames(t *testing.T) {
	for _, tc := range []struct {
		stage, want       string
		registered, ready int
	}{
		{"tenant-kubeconfig", "guest boot and node readiness not yet observed", 0, 0},
		{"tenant-api", "successful tenant API node query", 0, 0},
		{"tenant-nodes", "1/2 registered, 0/2 Ready", 1, 0},
		{"tenant-nodes", "2/2 registered, 1/2 Ready", 2, 1},
		{"tenant-nodes", "workload and DNS checks still required", 2, 2},
		{"tenant-workload", "successful completion still required", 2, 2},
		{"unknown-private-value", "", 0, 0},
		{"tenant-nodes", "", 3, 2},
		{"tenant-nodes", "", 1, 2},
	} {
		t.Run(tc.stage+tc.want, func(t *testing.T) {
			var out bytes.Buffer
			err := runProgress(t.Context(), &out, false, func(_ context.Context, writer io.Writer) error {
				writer.(nvkedemo.ProgressReporter).Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: tc.stage, Status: "private-status", RegisteredNodes: tc.registered, ReadyNodes: tc.ready, Elapsed: 20 * time.Minute, Remaining: 40 * time.Minute})
				return nil
			})
			if err != nil || tc.want == "" && out.Len() != 0 || tc.want != "" && (!strings.Contains(out.String(), tc.want) || !strings.Contains(out.String(), "20m0s elapsed · 40m0s remaining")) || strings.Contains(out.String(), "private-") {
				t.Fatalf("unhelpful or private output: %q error=%v", out.String(), err)
			}
		})
	}
}

func TestDashboardProgressOmitsMechanicalTraceButKeepsFailures(t *testing.T) {
	for _, dashboardMode := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "dashboard"}[dashboardMode], func(t *testing.T) {
			var out bytes.Buffer
			_, cancel := context.WithCancel(t.Context())
			defer cancel()
			w := &checkedWriter{out: &out, started: time.Now(), phases: make(map[string]time.Time), cancel: cancel}
			var view *dashboard
			if dashboardMode {
				view = newDashboard()
				w.observer = view
			}
			w.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseStarted, Name: "native-site-guest"})
			w.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.PhaseFinished, Name: "native-site-guest"})
			w.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-kubeconfig", Elapsed: time.Minute, Remaining: 59 * time.Minute})
			w.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.DeploymentFailed, Name: "private-check"})
			if !strings.Contains(out.String(), "[FAIL 0s] private-check") || w.failedPhase != "private-check" {
				t.Fatalf("failure lost: %q", out.String())
			}
			for _, marker := range []string{"[START", "[DONE", "Tenant  "} {
				if strings.Contains(out.String(), marker) == dashboardMode {
					t.Fatalf("unexpected mechanical trace for dashboard=%v: %q", dashboardMode, out.String())
				}
			}
			if dashboardMode && (view.siteStages[0].state != "observed" || view.tenantStages[0].state != "running") {
				t.Fatal("suppressed trace lost typed checklist observations")
			}
		})
	}
}

func TestProgressGuestDescriptionsUseClosedPublicFieldsOnly(t *testing.T) {
	for _, dashboardMode := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "dashboard"}[dashboardMode], func(t *testing.T) {
			var out bytes.Buffer
			_, cancel := context.WithCancel(t.Context())
			defer cancel()
			writer := &checkedWriter{out: &out, started: time.Now(), cancel: cancel}
			view := newDashboard()
			if dashboardMode {
				writer.observer = view
			}
			event := dashboardGuestFixture()
			event.Name, event.Status = "private-id", "private-status\x1b[2J"
			writer.Report(event)
			if dashboardMode {
				if out.Len() != 0 || view.snapshot().guestsObservedAt.IsZero() {
					t.Fatal("guest event was not routed directly to dashboard")
				}
			} else {
				for _, want := range []string{"Control plane: NICo Ready", "Worker: NICo Ready", "Infra Ready=True; VM running", "console observed=true (not Kubernetes Ready)"} {
					if !strings.Contains(out.String(), want) {
						t.Fatalf("missing closed public description %q: %s", want, out.String())
					}
				}
			}
			if strings.Contains(out.String(), "private-") || strings.ContainsRune(out.String(), '\x1b') {
				t.Fatal("private or terminal control fields exposed")
			}
			before := out.String()
			observed := view.snapshot().guestsObservedAt
			event.Guests[1].Domain = "private-invalid-domain"
			writer.Report(event)
			if out.String() != before || !view.snapshot().guestsObservedAt.Equal(observed) {
				t.Fatal("invalid guest descriptions reached the presentation boundary")
			}
		})
	}
}

func TestProgressGuestProviderFailureWithoutInstance(t *testing.T) {
	var out bytes.Buffer
	_, cancel := context.WithCancel(t.Context())
	defer cancel()
	writer := &checkedWriter{out: &out, started: time.Now(), cancel: cancel}
	writer.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantGuests, Name: "private-id", Status: "private-error", Guests: [3]nvkedemo.TenantGuestObservation{
		{Role: nvkedemo.TenantGuestControlPlane},
		{Role: nvkedemo.TenantGuestWorker, ProvisioningFailure: nvkedemo.TenantInstanceCreateFailed},
	}})
	if !strings.Contains(out.String(), "Worker: instance lookup/create failed (provider report; not a Kubernetes readiness result)") || strings.Contains(out.String(), "private-") {
		t.Fatalf("provider failure hidden or private data exposed: %q", out.String())
	}
}

func TestProgressGuestsEmitOnlyChangesOrObservedGrowth(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*nvkedemo.ProgressEvent)
		rows   int
		growth bool
	}{
		{"identical snapshot", func(*nvkedemo.ProgressEvent) {}, 0, false},
		{"age only", func(e *nvkedemo.ProgressEvent) { e.Guests[0].ConsoleLastChange += time.Minute }, 0, false},
		{"changed marker without growth", func(e *nvkedemo.ProgressEvent) { e.Guests[1].ConsoleChanged = true }, 0, false},
		{"domain change", func(e *nvkedemo.ProgressEvent) { e.Guests[1].Domain = nvkedemo.TenantDomainRunning }, 1, false},
		{"provider change", func(e *nvkedemo.ProgressEvent) { e.Guests[1].ProviderReady = nvkedemo.TenantConditionTrue }, 1, false},
		{"provider failure", func(e *nvkedemo.ProgressEvent) { e.Guests[1].ProvisioningFailure = nvkedemo.TenantInstanceCreateFailed }, 1, false},
		{"observation lost", func(e *nvkedemo.ProgressEvent) {
			e.Guests[1] = nvkedemo.TenantGuestObservation{Role: nvkedemo.TenantGuestWorker}
		}, 1, false},
		{"console observation lost", func(e *nvkedemo.ProgressEvent) {
			e.Guests[0].ConsoleKnown, e.Guests[0].ConsoleChanged = false, false
			e.Guests[0].ConsoleBytes, e.Guests[0].ConsoleLastChange = 0, 0
		}, 1, false},
		{"console grew", func(e *nvkedemo.ProgressEvent) {
			e.Guests[0].ConsoleBytes += 1024
			e.Guests[0].ConsoleLastChange = 3 * time.Second
		}, 1, true},
		{"console reset baseline", func(e *nvkedemo.ProgressEvent) { e.Guests[0].ConsoleBytes = 0 }, 0, false},
		{"invalid observation", func(e *nvkedemo.ProgressEvent) { e.Guests[0].Domain = "private-invalid" }, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			writer := &checkedWriter{out: &out, started: time.Now()}
			event := dashboardGuestFixture()
			writer.Report(event)
			if strings.Count(out.String(), "\n") != 2 || strings.Contains(out.String(), "bytes grew") {
				t.Fatalf("first snapshot must be descriptive, not growth: %q", out.String())
			}
			out.Reset()
			tc.change(&event)
			writer.Report(event)
			text := out.String()
			if strings.Count(text, "\n") != tc.rows || strings.Contains(text, "bytes grew") != tc.growth {
				t.Fatalf("rows/growth mismatch: %q", text)
			}
			if tc.rows > 0 && !strings.HasPrefix(text, "  Tenant guest +") {
				t.Fatal("changed observation omitted elapsed tenant context")
			}
			if tc.growth && !strings.Contains(text, "growth age 3s at snapshot (not guest health)") {
				t.Fatal("growth omitted snapshot-relative descriptive scope")
			}
			out.Reset()
			writer.Report(event)
			if out.Len() != 0 {
				t.Fatal("unchanged snapshot repeated its role rows")
			}
		})
	}
}

func TestProgressReportsConformanceThreeNodeDenominator(t *testing.T) {
	for _, expected := range []int{2, 3} {
		t.Run(fmt.Sprint(expected), func(t *testing.T) {
			var out bytes.Buffer
			w := &checkedWriter{out: &out, started: time.Now()}
			w.Report(nvkedemo.ProgressEvent{Kind: nvkedemo.TenantWaiting, Name: "tenant-nodes", ExpectedNodes: expected, RegisteredNodes: expected, ReadyNodes: expected, Remaining: time.Hour})
			if !strings.Contains(out.String(), fmt.Sprintf("%d/%d Ready", expected, expected)) {
				t.Fatalf("wrong node denominator: %s", out.String())
			}
		})
	}
}
