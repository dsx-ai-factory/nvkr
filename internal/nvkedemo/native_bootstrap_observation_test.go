// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

type bootstrapObservationRecorder struct {
	bytes.Buffer
	terminalDebug bool
}

func (r *bootstrapObservationRecorder) nativeBootstrapTerminalEnabled() bool { return r.terminalDebug }

func (r *bootstrapObservationRecorder) observeNativeBootstrap(text string) {
	r.WriteString(text)
}

func TestNativeBootstrapLogSummaryPublishesOnlySourceEnums(t *testing.T) {
	const stage = `{"operation":"bootstrap_site","msg":"installing postgres","err":"PRIVATE_TOKEN","cluster":"PRIVATE_CLUSTER"}`
	const phase = `{"operation":"bootstrap_site","msg":"phase finished","phase":"engine_deploy","outcome":"failed","err":"PRIVATE_TOKEN"}`
	for _, tc := range []struct{ name, raw, want string }{
		{"source records", stage + "\n" + phase, "Bootstrap stage observation: installing postgres\nBootstrap phase observation: engine_deploy failed\n"},
		{"cluster state", `{"operation":"bootstrap_site","msg":"cluster state","status":"ProvisioningFailed","cluster":"PRIVATE_NAME"}`, "Bootstrap cluster state observation: ProvisioningFailed\n"},
		{"unknown cluster state", `{"operation":"bootstrap_site","msg":"cluster state","status":"PRIVATE_STATE"}`, ""},
		{"arbitrary output", "PRIVATE_CONFIG\nPRIVATE_ERROR", ""},
		{"unknown phase", strings.ReplaceAll(phase, "engine_deploy", "PRIVATE_PHASE"), ""},
		{"unknown outcome", strings.ReplaceAll(phase, "failed", "PRIVATE_OUTCOME"), ""},
		{"unknown operation", strings.ReplaceAll(stage, "bootstrap_site", "PRIVATE_OPERATION"), ""},
		{"unknown message", strings.ReplaceAll(stage, "installing postgres", "PRIVATE_MESSAGE"), ""},
		{"malformed JSON", `{"operation":"bootstrap_site","msg":`, ""},
		{"oversized record", strings.Repeat("x", 64<<10) + "\n" + phase, ""},
		{"bounded rows", strings.Repeat(stage+"\n", 80), strings.Repeat("Bootstrap stage observation: installing postgres\n", 64)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nativeBootstrapLogSummary([]byte(tc.raw)); got != tc.want || strings.Contains(got, "PRIVATE_") {
				t.Fatalf("unsafe or incorrect projection: %q", got)
			}
		})
	}
}

func TestRunNativeBootstrapRetainsPrivateOutputAndOriginalResult(t *testing.T) {
	for _, mode := range []string{"success", "failure", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			root := privateTestDir(t)
			cli := filepath.Join(root, "nvkectl")
			writeTenantFixture(t, cli, []byte(`#!/bin/sh
set -eu
test "$1" = --json-logs
test "$2" = bootstrap
test "$3" = site
printf 'PRIVATE_SITESPEC\n'
printf '%s\n' '{"operation":"bootstrap_site","msg":"phase finished","phase":"preflight","outcome":"succeeded","err":"PRIVATE_STDERR"}' >&2
if test "$MODE" = failure; then exit 7; fi
if test "$MODE" = canceled; then sleep 30; fi
`), 0o700)
			var progress bootstrapObservationRecorder
			var originalOutput bytes.Buffer
			runner := Runner{Work: root, Env: []string{"PATH=/usr/bin:/bin", "MODE=" + mode}, Progress: &progress, output: &originalOutput}
			duration := 5 * time.Second
			if mode == "canceled" {
				duration = 100 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(t.Context(), duration)
			defer cancel()
			err := runNativeBootstrap(ctx, runner, []string{cli, "bootstrap", "site"}, nil)
			if mode == "success" && err != nil || mode == "failure" && CommandExitCode(err) != 7 || mode == "canceled" && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("original result changed: %v", err)
			}
			if errors.Is(err, ErrUnjoined) || strings.Contains(progress.String(), "PRIVATE_") || originalOutput.Len() != 0 {
				t.Fatalf("output leak or unresolved process: %q %v", progress.String(), err)
			}
			// Cancellation can precede the child writing any observation. Its
			// required contract is the joined cancellation and private output.
			if mode == "canceled" {
				return
			}
			if !strings.Contains(progress.String(), "Bootstrap phase observation: preflight succeeded") {
				t.Fatal("source phase observation lost")
			}
			raw, err := os.ReadFile(filepath.Join(root, "host-nvke-bootstrap.log"))
			if err != nil || !bytes.Contains(raw, []byte("PRIVATE_SITESPEC")) || !bytes.Contains(raw, []byte("PRIVATE_STDERR")) {
				t.Fatal("private evidence lost")
			}
		})
	}
}

func TestNativeBootstrapFailureObservationPreservesCauseAndPrivacy(t *testing.T) {
	for _, mode := range []string{"observed", "read failure", "unjoined cause", "no progress", "plain writer", "success"} {
		t.Run(mode, func(t *testing.T) {
			root := privateTestDir(t)
			writeTenantFixture(t, filepath.Join(root, "limactl"), []byte(`#!/bin/sh
set -eu
test "$1" = --log-level=error
test "$2" = --tty=false
test "$3" = shell
test "$4" = --workdir=/
test "$5" = bootstrap
if test "$MODE" = 'read failure'; then exit 9; fi
printf 'capacity-sample\n'
`), 0o700)
			writeTenantFixture(t, filepath.Join(root, "kind"), []byte("#!/bin/sh\nprintf 'PRIVATE_KUBECONFIG\\n'\n"), 0o700)
			writeTenantFixture(t, filepath.Join(root, "kubectl"), []byte(`#!/bin/sh
if test "$MODE" = 'read failure'; then exit 9; fi
case "$*" in
 *"get --raw /api/v1/namespaces/nvke-controller/services/nvke-controller:http/proxy/api/clusters")
  printf '%s\n' '{"clusters":[{"name":"PRIVATE_CLUSTER","conditions":{"provisioning":{"status":"False","message":"Workflow failed: failed to resolve the CNI install mode: PRIVATE_TOKEN"}}}]}'
  exit 0;;
 *"get --raw /api/v1/namespaces/nvke-controller/services/nvke-controller:http/proxy/api/nodes")
  printf '%s\n' '{"nodes":[{"name":"PRIVATE_NODE","conditions":{"readyToJoin":{"status":"True"}}}]}'
  exit 0;;
esac
printf '%s\n' '{"items":[{"metadata":{"namespace":"default","name":"PRIVATE_POD"},"spec":{"secret":"PRIVATE_SECRET"},"status":{"phase":"Pending","containerStatuses":[{"ready":false,"restartCount":0}]}}]}'
`), 0o700)
			var progress bootstrapObservationRecorder
			runner := Runner{Work: root, Env: []string{"PATH=/usr/bin:/bin", "MODE=" + mode}, Progress: &progress}
			attempt := nativeSiteAttempt{assets: NativeAssets{HostTools: root}, bootstrap: &hostBootstrap{limactl: filepath.Join(root, "limactl")}, bootstrapName: "owned"}
			cause := errors.New("original bootstrap failure")
			if mode == "unjoined cause" {
				cause = errors.Join(cause, ErrUnjoined)
			}
			if mode == "no progress" {
				runner.Progress = nil
			}
			if mode == "plain writer" {
				runner.Progress = &progress.Buffer
			}
			if mode == "success" {
				cause = nil
			}
			if got := attempt.observeBootstrapFailure(t.Context(), runner, cause); got != cause {
				t.Fatalf("primary result changed: %v", got)
			}
			if strings.Contains(progress.String(), "PRIVATE_") {
				t.Fatalf("private fields escaped: %q", progress.String())
			}
			if mode == "observed" && (!strings.Contains(progress.String(), "capacity-sample") || !strings.Contains(progress.String(), "pod-001  Pending  0/1") || !strings.Contains(progress.String(), "clusters[1] provisioning=False") || !strings.Contains(progress.String(), "nodes[1] readyToJoin=True") || !strings.Contains(progress.String(), "Bootstrap source failure context: cni-mode")) {
				t.Fatalf("bounded observations missing: %q", progress.String())
			}
			if mode != "observed" && mode != "read failure" && progress.Len() != 0 {
				t.Fatal("ineligible outcome started observations")
			}
		})
	}
}

func TestNativeBootstrapTerminalReportRequiresReviewedSourceAndFailure(t *testing.T) {
	const line = "Error: failed to initialize Kubernetes control plane: kubeadm init failed: error execution phase wait-control-plane: cannot obtain client without bootstrap (credential <redacted>)"
	source := &nativeInstallerSelection{commit: reviewedBootstrapTerminalSource, terminalCLICommit: reviewedBootstrapTerminalSource, terminalAgentCommit: reviewedBootstrapTerminalSource}
	for _, tc := range []struct {
		name   string
		source *nativeInstallerSelection
		raw    string
		valid  bool
	}{
		{"reviewed operator report", source, line, true},
		{"raw JSON stays private", source, `{"msg":"PRIVATE_TOKEN","error":"PRIVATE_SECRET"}` + "\n" + line, true},
		{"missing selection", nil, line, false},
		{"patched CLI", &nativeInstallerSelection{commit: reviewedBootstrapTerminalSource, terminalAgentCommit: reviewedBootstrapTerminalSource}, line, false},
		{"patched agent", &nativeInstallerSelection{commit: reviewedBootstrapTerminalSource, terminalCLICommit: reviewedBootstrapTerminalSource}, line, false},
		{"other source", &nativeInstallerSelection{commit: strings.Repeat("a", 40)}, line, false},
		{"other error", source, "Error: PRIVATE_TOKEN", false},
		{"arbitrary stderr", source, "PRIVATE_TOKEN", false},
		{"JSON attribute", source, `{"msg":"PRIVATE","error":` + strconv.Quote(line) + `}`, false},
		{"oversized report", source, line + strings.Repeat("x", 4096), false},
		{"ambiguous reports", source, line + "\n" + line, false},
		{"oversized preceding log", source, strings.Repeat("x", 40<<10) + "\n" + line, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := nativeBootstrapTerminalFailure(tc.source, []byte(tc.raw))
			want := ""
			if tc.valid {
				want = fmt.Sprintf("NVKE terminal failure (source-redacted): %q\n", line)
			}
			if got != want || strings.Contains(got, "PRIVATE_") {
				t.Fatalf("unsafe or incorrect report: %q", got)
			}
		})
	}
	controlled := line + "\r::error::untrusted"
	if got := nativeBootstrapTerminalFailure(source, []byte(controlled)); !strings.Contains(got, `\r::error::untrusted`) || strings.Contains(got, "\r") {
		t.Fatalf("terminal controls were not quoted: %q", got)
	}
}

func TestRunNativeBootstrapReportsReviewedFailureOnlyAfterFailedJoin(t *testing.T) {
	const report = "Error: failed to initialize Kubernetes control plane: kubeadm init failed: error execution phase wait-control-plane: cannot obtain client without bootstrap"
	for _, mode := range []string{"success", "failure", "disabled", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			root := privateTestDir(t)
			cli := filepath.Join(root, "nvkectl")
			writeTenantFixture(t, cli, []byte("#!/bin/sh\nset -eu\nprintf 'PRIVATE_SITESPEC\\n'\nprintf '%s\\n' "+operatorQuote(report)+" >&2\nif test \"$MODE\" = failure || test \"$MODE\" = disabled; then exit 7; fi\nif test \"$MODE\" = canceled; then sleep 30; fi\n"), 0700)
			progress := bootstrapObservationRecorder{terminalDebug: mode != "disabled"}
			runner := Runner{Work: root, Env: []string{"PATH=/usr/bin:/bin", "MODE=" + mode}, Progress: &progress}
			duration := 5 * time.Second
			if mode == "canceled" {
				duration = 100 * time.Millisecond
			}
			ctx, cancel := context.WithTimeout(t.Context(), duration)
			defer cancel()
			err := runNativeBootstrap(ctx, runner, []string{cli, "bootstrap", "site"}, &nativeInstallerSelection{commit: reviewedBootstrapTerminalSource, terminalCLICommit: reviewedBootstrapTerminalSource, terminalAgentCommit: reviewedBootstrapTerminalSource})
			if mode == "success" && err != nil || (mode == "failure" || mode == "disabled") && CommandExitCode(err) != 7 || mode == "canceled" && !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrUnjoined) {
				t.Fatalf("original result changed: %v", err)
			}
			if strings.Contains(progress.String(), "PRIVATE_") {
				t.Fatal("private stdout was presented")
			}
			if (mode == "success" || mode == "disabled") && progress.Len() != 0 || mode == "failure" && !strings.Contains(progress.String(), "cannot obtain client without bootstrap") {
				t.Fatalf("incorrect operator report: %q", progress.String())
			}
		})
	}
}
