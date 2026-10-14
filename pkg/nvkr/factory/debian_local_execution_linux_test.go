// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package factory

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// Exercises the public scheduler with real pinned Git closure capture and a
// controlled native runtime. Archive bytes and scanner reports are fixtures;
// this does not prove a real Debian package or NVSentinel installation.
func debianSchedulerNativeFixture(t *testing.T, ctx context.Context) (*Plan, string, string) {
	t.Helper()
	root, err := os.MkdirTemp(os.Getenv("NVKR_TEST_DEBIAN_DISK_SCRATCH"), "debian-scheduler-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	controlled := strings.Replace(publicDiskEvidenceRuntimeSource,
		`kind=="disk-image"||kind=="installer-iso"||kind=="apt-acquisition"`,
		`kind=="disk-image"||kind=="installer-iso"||kind=="apt-acquisition"||kind=="host-package"`, 1)
	controlled = strings.ReplaceAll(controlled, `"nvkr:diskContentClass"`, `"nkd:diskContentClass"`)
	controlled = strings.ReplaceAll(controlled, `"nvkr:packageArchitecture"`, `"nkd:packageArchitecture"`)
	controlled = strings.ReplaceAll(controlled, `"nvkr:packageFormat"`, `"nkd:packageFormat"`)
	controlled = strings.Replace(controlled, `if kind=="apt-acquisition"{`, `if kind=="apt-acquisition"||kind=="host-package"{`, 1)
	controlled = strings.Replace(controlled, "\ncase \"create\":", `
case "run":name:=value(a,"--name");put("container-"+name,a);put("container-"+id(name),a);out:="";for i,arg:=range a {if arg=="-v"&&i+1<len(a)&&strings.HasSuffix(a[i+1],":/src/out"){out=strings.TrimSuffix(a[i+1],":/src/out")}};if out==""{os.Exit(96)};fail(os.WriteFile(filepath.Join(out,"agent-"+runtime.GOARCH+".deb"),[]byte("controlled final archive"),0600))
case "create":`, 1)
	controlled = strings.Replace(controlled, `case "rm":for _,target`, `case "rm":if mode=="producer cleanup failed"{os.Exit(7)};for _,target`, 1)
	source := filepath.Join(root, "runtime.go")
	if err := os.WriteFile(source, []byte(controlled), 0600); err != nil {
		t.Fatal(err)
	}
	clientPath := filepath.Join(root, "docker")
	command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", "-X main.fixtureRoot="+root, "-o", clientPath, source)
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if raw, err := command.CombinedOutput(); err != nil {
		t.Fatalf("controlled runtime ELF: %v %s", err, raw)
	}
	raw, err := os.ReadFile(clientPath)
	if err != nil {
		t.Fatal(err)
	}
	pin := pipeline.FileDigest(raw)
	for name, value := range map[string]string{"mode": "success", "pin": pin} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(raw) }))
	t.Cleanup(server.Close)
	// Trust this fixture's exact TLS certificate through the standard transport;
	// never disable certificate verification or change production acquisition.
	previousTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	d, env, stack := debianFactoryInputs(t)
	platform := "linux/" + runtime.GOARCH
	d.Outputs[0].Platform, d.SBOMTool = platform, "syft"
	env.UtilityPlatform, env.Runtime.Runtime = platform, "docker"
	env.DiskInspectionProfiles = map[string]definition.DiskInspectionProfile{platform: {Image: "example.test/inspection@" + pin, InspectorDigest: pin, CodecDigest: pin, GuestfishDigest: pin, ScannerDigest: pin, PackageReaderDigest: pin, Resources: execution.ResourceRequest{CPUMillis: 1000, MemoryMiB: 9000, EphemeralStorageMiB: 4096}, TimeoutSeconds: 30, MaxFilesystemBytes: 1 << 20, ScratchStorage: "private-volume"}}
	t.Chdir(root)
	preseed := filepath.Join(root, "pinned-source")
	if err := os.Mkdir(preseed, 0700); err != nil {
		t.Fatal(err)
	}
	debianBridgeGit(t, ctx, preseed, "init", "-q")
	if err := os.WriteFile(filepath.Join(preseed, "input.txt"), []byte("controlled exact source\n"), 0600); err != nil {
		t.Fatal(err)
	}
	debianBridgeGit(t, ctx, preseed, "add", "input.txt")
	debianBridgeGit(t, ctx, preseed, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "controlled source")
	component := stack.Catalog.Components["agent"]
	component.Commit = debianBridgeGit(t, ctx, preseed, "rev-parse", "HEAD")
	component.TreeHash = debianBridgeGit(t, ctx, preseed, "rev-parse", "HEAD^{tree}")
	stack, err = catalog.ApplyStack(&catalog.Catalog{Components: map[string]catalog.Component{"agent": component}, Tools: map[string]catalog.ToolPin{"syft": {Format: "raw", URL: server.URL, Platform: platform, ExecutablePath: "syft", ArchiveDigest: pin, ExecutableDigest: pin}}})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Compile(ctx, d, env, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	workspace := ".nvkr/native-scheduler"
	digest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureLocalWorkspace(ctx, workspace, digest); err != nil {
		t.Fatal(err)
	}
	_, artifacts := planIndex(plan)
	for _, node := range plan.Graph.Nodes {
		if node.Kind == NodeFetch {
			path, err := relocatedFactoryPath(artifacts["source/"+node.BuildTarget+"/"+node.IRNode].Path, workspace)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(preseed, path); err != nil {
				t.Fatal(err)
			}
		}
	}
	return plan, clientPath, root
}

func TestDebianSchedulerNativeOriginalRunExport(t *testing.T) {
	if os.Getenv("NVKR_TEST_DEBIAN_CONTENT_RUNTIME") != "1" {
		t.Skip("explicit native scheduler proof needs actual inspection capacity")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	plan, clientPath, _ := debianSchedulerNativeFixture(t, ctx)
	var log bytes.Buffer
	result, err := RunLocal(ctx, plan, LocalRunOptions{Runtime: clientPath, Workspace: ".nvkr/native-scheduler", Stdout: &log, Stderr: &log})
	if err != nil {
		t.Fatalf("public native scheduler: %v\n%s", err, log.String())
	}
	defer result.Close()
	if len(result.Nodes) != len(plan.Graph.Nodes) {
		t.Fatal("scheduler omitted graph work")
	}
	for _, node := range result.Nodes {
		if node.Status != "ok" {
			t.Fatalf("node %s incomplete: %s", node.ID, node.Status)
		}
	}
	var archive bytes.Buffer
	observed, document, predicate, err := result.ExportDebianLifecyclePayload(ctx, plan, plan.LifecycleOutputs[0], &archive)
	if err != nil || archive.String() != "controlled final archive" || observed.Digest != pipeline.FileDigest(archive.Bytes()) || len(document) == 0 || len(predicate) == 0 {
		t.Fatal("original scheduler export lost payload/evidence", err)
	}
	if err := result.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := result.ExportDebianLifecyclePayload(ctx, plan, plan.LifecycleOutputs[0], io.Discard); factoryErrorCode(err) != e.CodeEngineCapability {
		t.Fatal("closed scheduler Run retained export authority", err)
	}
}

func TestDebianSchedulerWrongRuntimeRejectsBeforeProbeOrWorkspaceNative(t *testing.T) {
	d, env, stack := debianFactoryInputs(t)
	platform := "linux/" + runtime.GOARCH
	d.Outputs[0].Platform = platform
	d.SBOMTool = "syft"
	env.UtilityPlatform = platform
	env.Runtime.Runtime = "docker"
	pin := "sha256:" + strings.Repeat("d", 64)
	env.DiskInspectionProfiles = map[string]definition.DiskInspectionProfile{platform: {Image: "example.test/inspection@" + pin, InspectorDigest: pin, CodecDigest: pin, GuestfishDigest: pin, ScannerDigest: pin, PackageReaderDigest: pin, Resources: execution.ResourceRequest{CPUMillis: 1000, MemoryMiB: 9000, EphemeralStorageMiB: 4096}, TimeoutSeconds: 30, MaxFilesystemBytes: 1 << 20, ScratchStorage: "private-volume"}}
	stack.Catalog.Tools = map[string]catalog.ToolPin{"syft": {Format: "raw", URL: "https://example.test/syft", Platform: platform, ExecutablePath: "syft", ArchiveDigest: pin, ExecutableDigest: pin}}
	stack, err := catalog.ApplyStack(stack.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	plan, err := Compile(ctx, d, env, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	fakeRuntime := filepath.Join(t.TempDir(), "podman")
	marker := filepath.Join(t.TempDir(), "unexpected-probe")
	if err := os.WriteFile(fakeRuntime, []byte("#!/bin/sh\nprintf unexpected > '"+marker+"'\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	options := LocalRunOptions{Runtime: fakeRuntime, Workspace: ".nvkr/native-mismatch"}
	for _, mode := range []string{"public admission", "public Run"} {
		t.Run(mode, func(t *testing.T) {
			var err error
			if mode == "public admission" {
				err = AdmitLocal(ctx, plan, options)
			} else {
				var result *LocalRunResult
				result, err = RunLocal(ctx, plan, options)
				if result != nil {
					result.Close()
					t.Fatal("mismatched runtime returned Run")
				}
			}
			if factoryErrorCode(err) != e.CodeEngineCapability {
				t.Fatal("wrong runtime not rejected", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("runtime probe occurred")
			}
			if _, err := os.Stat(".nvkr"); !os.IsNotExist(err) {
				t.Fatal("workspace work occurred")
			}
		})
	}
}

func TestDebianSchedulerNativeFailureReturnsNoRun(t *testing.T) {
	if os.Getenv("NVKR_TEST_DEBIAN_CONTENT_RUNTIME") != "1" {
		t.Skip("explicit native scheduler proof needs actual inspection capacity")
	}
	for _, mode := range []string{"producer cleanup failed", "cancel during root scan"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			plan, clientPath, root := debianSchedulerNativeFixture(t, ctx)
			if err := os.WriteFile(filepath.Join(root, "mode"), []byte(mode), 0600); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			var result *LocalRunResult
			var runErr error
			go func() {
				result, runErr = RunLocal(ctx, plan, LocalRunOptions{Runtime: clientPath, Workspace: ".nvkr/native-scheduler"})
				close(done)
			}()
			want := e.CodeBuildFailed
			if mode == "cancel during root scan" {
				want = e.CodeTimeout
				poll := time.NewTicker(time.Millisecond)
				defer poll.Stop()
			waitScan:
				for {
					select {
					case <-ctx.Done():
						t.Fatal("scan did not begin", ctx.Err())
					case <-done:
						t.Fatal("Run ended before cancellation marker", runErr)
					case <-poll.C:
						if _, err := os.Stat(filepath.Join(root, "scan-started")); err == nil {
							cancel()
							break waitScan
						}
					}
				}
			}
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("Run failure did not join owned work")
			}
			if result != nil || factoryErrorCode(runErr) != want {
				t.Fatal("failed scheduler returned success or wrong code", runErr)
			}
			if _, err := os.Stat(filepath.Join(".nvkr", "native-scheduler", "run.json")); !os.IsNotExist(err) {
				t.Fatal("failed scheduler persisted successful record", err)
			}
			if mode == "cancel during root scan" {
				entries, err := os.ReadDir(root)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), "container-") || strings.HasPrefix(entry.Name(), "volume-") {
						t.Fatal("canceled Run left unjoined owned runtime resource", entry.Name())
					}
				}
			}
		})
	}
}

func TestDebianSchedulerNativeFreezesPlanBeforeAdmission(t *testing.T) {
	if os.Getenv("NVKR_TEST_DEBIAN_CONTENT_RUNTIME") != "1" {
		t.Skip("explicit native scheduler proof needs actual inspection capacity")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	plan, clientPath, _ := debianSchedulerNativeFixture(t, ctx)
	original := cloneDebianTestPlan(t, plan)
	barrier := &debianProvenanceBarrierContext{Context: ctx, started: make(chan struct{}), release: make(chan struct{})}
	done := make(chan struct{})
	var result *LocalRunResult
	var runErr error
	go func() {
		result, runErr = RunLocal(barrier, plan, LocalRunOptions{Runtime: clientPath, Workspace: ".nvkr/native-scheduler"})
		close(done)
	}()
	select {
	case <-barrier.started:
	case <-ctx.Done():
		t.Fatal("admission barrier did not begin", ctx.Err())
	}
	// The barrier's channel synchronizes with the earlier canonical copy. The
	// caller's authored description can now change without changing execution.
	plan.Environment.UtilityPlatform = "linux/invalid"
	plan.Definition.Outputs[0].Components = []string{"foreign"}
	close(barrier.release)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("frozen scheduler did not finish", ctx.Err())
	}
	if runErr != nil || result == nil {
		t.Fatal("caller mutation changed frozen execution", runErr)
	}
	defer result.Close()
	var archive bytes.Buffer
	if _, _, _, err := result.ExportDebianLifecyclePayload(ctx, original, original.LifecycleOutputs[0], &archive); err != nil || archive.String() != "controlled final archive" {
		t.Fatal("original frozen output lost export", err)
	}
	if _, _, _, err := result.ExportDebianLifecyclePayload(ctx, plan, original.LifecycleOutputs[0], io.Discard); factoryErrorCode(err) != e.CodeVerifyFailed {
		t.Fatal("mutated caller Plan acquired export authority", err)
	}
}
