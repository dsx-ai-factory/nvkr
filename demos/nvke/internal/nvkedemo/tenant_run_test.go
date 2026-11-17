// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Shared subprocess answers for the actual generic CAPI tenant/session tests.
// These tools never start a guest or claim real cluster acceptance.
func tenantRunFixture(t *testing.T, fail, block string) (tenant, string) {
	t.Helper()
	root := privateTestDir(t)
	work := filepath.Join(root, "site")
	for _, path := range []string{work, filepath.Join(work, "cli")} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeTenantFixture(t, filepath.Join(work, "cli", installerCLIName), []byte(`#!/bin/sh
set -eu
case "$1 $2" in
  'get clusters') if test -f "$FIXTURE/created"; then cat "$FIXTURE/rows.json"; else printf '[]\n'; fi ;;
  'get nodepools') cat "$FIXTURE/pools.json" ;;
  'create cluster') : > "$FIXTURE/created" ;;
  'get kubeconfig') printf 'fixture only\n' > "$5" ;;
  *) exit 22 ;;
esac
`), 0o700)
	writeTenantFixture(t, filepath.Join(work, "kubectl"), []byte(`#!/bin/sh
set -eu
step() {
  printf '%s\n' "$1" >> "$FIXTURE/events"
  if test "$FAIL" = "$1"; then exit 7; fi
  if test "$BLOCK" = "$1"; then : > "$FIXTURE/started"; exec sleep 60; fi
}
test "$1" = --kubeconfig
shift 2
case "$1" in --request-timeout=*) shift ;; esac
if test "$1" = -n; then shift 2; fi
if test -n "${CLOUDINIT_TOOL:-}"; then
  case "$1 $2" in
    'get namespace'|'create -f'|'get pod'|'logs status-'*|'delete --raw=/api/v1/namespaces/nvkr-cloud-init-'*) exec "$CLOUDINIT_TOOL" -test.run=TestNativeCloudInitFixtureTool -- "$@" ;;
  esac
fi
case "$1 $2" in
  'get pods') step workload-pod; cat "$FIXTURE/pods.json" ;;
  'get secret') step kubeconfig; printf '{"data":{"value":"YQ=="}}\n' ;;
  'get nodes') step nodes; cat "$FIXTURE/nodes.json" ;;
  'get machines.cluster.x-k8s.io,nicomachines.infrastructure.cluster.x-k8s.io') step guest-bindings; printf '{"items":[]}\n' ;;
  'rollout status') step "$3" ;;
  'apply -f') step workload-apply ;;
  'wait job/nvkr-smoke') step workload-complete ;;
  'logs job/nvkr-smoke') step workload-log; printf 'NVKE_LOCAL_WORKLOAD_PASS\n' ;;
  *) exit 22 ;;
esac
`), 0o700)
	runner := Runner{Work: work, Env: []string{"PATH=/usr/bin:/bin", "FIXTURE=" + root, "FAIL=" + fail, "BLOCK=" + block}}
	return tenant{name: "owned", session: ManagementSession{Work: work, Tools: work, Runner: runner}}, root
}

func TestNativeSessionErrorPreservesCodesAndUnjoinedCauses(t *testing.T) {
	sentinel := errors.New("fixture failure")
	for _, test := range []struct {
		name  string
		cause error
		code  nvkrerr.Code
	}{
		{"ordinary", sentinel, nvkrerr.CodePreflightFailed},
		{"capability", nvkrerr.Wrap(nvkrerr.CodeEngineCapability, sentinel, "fixture capability"), nvkrerr.CodeEngineCapability},
		{"coded timeout", nvkrerr.Wrap(nvkrerr.CodeTimeout, sentinel, "fixture budget"), nvkrerr.CodeTimeout},
		{"canceled", errors.Join(ErrUnjoined, context.Canceled), nvkrerr.CodeTimeout},
		{"deadline", errors.Join(ErrUnjoined, context.DeadlineExceeded), nvkrerr.CodeTimeout},
		{"unjoined", ErrUnjoined, nvkrerr.CodePreflightFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			public := nativeSessionError(test.cause, "native tenant")
			var coded *nvkrerr.Error
			if !errors.As(public, &coded) || coded.Code != test.code || !errors.Is(public, test.cause) || errors.Is(public, ErrUnjoined) != errors.Is(test.cause, ErrUnjoined) {
				t.Fatalf("public wrapping lost original code, cause or cleanup retention: %v", public)
			}
		})
	}
	if nativeSessionError(nil, "native tenant") != nil {
		t.Fatal("success must remain success")
	}
}
