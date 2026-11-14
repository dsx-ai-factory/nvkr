// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func executionErrorCode(err error) nvkrerr.Code {
	var coded *nvkrerr.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

func TestObserveContainerImageMeasuresBareLocalImageID(t *testing.T) {
	runtimePath, _, _ := imageAdmissionRuntimeFixture(t)
	want := "sha256:" + repeatHex("b")
	node := ir.Node{ID: "build-local", Kind: ir.KindBuild, Toolchain: want, Platform: "linux/amd64"}
	got, err := observeContainerImage(t.Context(), runtimePath, node)
	if err != nil || got != want {
		t.Fatalf("local ID: %q %v", got, err)
	}
	node.Toolchain = "sha256:" + repeatHex("d")
	if _, err := observeContainerImage(t.Context(), runtimePath, node); executionErrorCode(err) != nvkrerr.CodePreflightFailed {
		t.Fatal(err)
	}
}

func TestRuntimeIdentityBindsEndpointAndServer(t *testing.T) {
	raw := []byte(`{"Client":{"Version":"1"},"Server":{"Version":"1"}}`)
	base, err := runtimeIdentityFromOutput("sha256:"+repeatHex("a"), qualifiedRuntimeEndpoint, "daemon-a", raw)
	if err != nil {
		t.Fatal(err)
	}
	otherEndpoint, err := runtimeIdentityFromOutput("sha256:"+repeatHex("a"), "unix:///run/other.sock", "daemon-a", raw)
	if err != nil {
		t.Fatal(err)
	}
	otherServer, err := runtimeIdentityFromOutput("sha256:"+repeatHex("a"), qualifiedRuntimeEndpoint, "daemon-b", raw)
	if err != nil {
		t.Fatal(err)
	}
	if base == otherEndpoint || base == otherServer || otherEndpoint == otherServer {
		t.Fatal("runtime identity omitted its exact endpoint or server identity")
	}
}

func TestRuntimeIdentityRejectsAmbiguousVersionJSON(t *testing.T) {
	_, err := runtimeIdentityFromOutput(
		"sha256:"+repeatHex("a"), qualifiedRuntimeEndpoint, "daemon-a",
		[]byte(`{"Server":{"Version":"trusted"},"Server":{"Version":"attacker"}}`),
	)
	if executionErrorCode(err) != nvkrerr.CodeEngineCapability {
		t.Fatalf("ambiguous runtime identity JSON did not fail closed: %v", err)
	}
}

func TestPodmanServerIdentityExcludesChangingCapacity(t *testing.T) {
	base := []byte(`{"host":{"arch":"arm64","cgroupManager":"systemd","cgroupVersion":"v2","databaseBackend":"sqlite","hostname":"builder","kernel":"6.1","networkBackend":"netavark","os":"linux","security":{"rootless":true,"seccompEnabled":true,"selinuxEnabled":false},"memAvailable":100,"uptime":"1m"},"store":{"graphDriverName":"overlay","graphRoot":"/var/lib/containers","runRoot":"/run/containers"},"version":{"APIVersion":"5.6.2","GitCommit":"abc","OsArch":"linux/arm64","Version":"5.6.2"}}`)
	changed := bytes.ReplaceAll(base, []byte(`"memAvailable":100,"uptime":"1m"`), []byte(`"memAvailable":1,"uptime":"2h"`))
	first, err := podmanServerIdentity(base)
	if err != nil {
		t.Fatal(err)
	}
	second, err := podmanServerIdentity(changed)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("Podman identity includes changing capacity or uptime")
	}
	other := bytes.ReplaceAll(base, []byte(`"graphRoot":"/var/lib/containers"`), []byte(`"graphRoot":"/other"`))
	third, err := podmanServerIdentity(other)
	if err != nil {
		t.Fatal(err)
	}
	if first == third {
		t.Fatal("Podman identity omitted its storage root")
	}
}

func TestRuntimeSnapshotClosesObserveThenExecutePathRace(t *testing.T) {
	original := filepath.Join(t.TempDir(), "runtime")
	if err := os.WriteFile(original, []byte("#!/bin/sh\nprintf original"), 0o755); err != nil {
		t.Fatal(err)
	}
	snapshot, err := snapshotRuntimeExecutableContext(t.Context(), original)
	if runtime.GOOS != "linux" {
		if executionErrorCode(err) != nvkrerr.CodeEngineCapability {
			t.Fatalf("non-Linux qualified runtime capture did not fail closed: %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.capability.Close()
	if err := os.WriteFile(original, []byte("#!/bin/sh\nprintf replaced"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := heldExecutionOutput(context.Background(), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "original" {
		t.Fatalf("snapshotted execution used replaced runtime bytes: %q", out)
	}
}
