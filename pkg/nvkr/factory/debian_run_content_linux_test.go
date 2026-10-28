// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package factory

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/execution"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

// Controlled native executable/daemon reports and arbitrary archive bytes prove
// ownership/document joins, not real Debian tooling or installation. The native
// positive tests are opt-in, like the existing disk issuer fixture: set
// NVKR_TEST_DEBIAN_CONTENT_RUNTIME=1 with actual host memory above its 9000 MiB
// profile. Requested runs fail on insufficient capacity rather than skipping.
func debianRunContentFixture(t *testing.T, ctx context.Context, mode string) (*Plan, *LocalRunResult, *sbom.DiskInspectionRuntime, string) {
	t.Helper()
	inspectionRoot := t.TempDir()
	if diskRoot := os.Getenv("NVKR_TEST_DEBIAN_DISK_SCRATCH"); diskRoot != "" {
		var err error
		inspectionRoot, err = os.MkdirTemp(diskRoot, "debian-content-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.RemoveAll(inspectionRoot); err != nil {
				t.Error(err)
			}
		})
	}
	source := filepath.Join(inspectionRoot, "runtime.go")
	controlled := strings.Replace(publicDiskEvidenceRuntimeSource,
		`kind=="disk-image"||kind=="installer-iso"||kind=="apt-acquisition"`,
		`kind=="disk-image"||kind=="installer-iso"||kind=="apt-acquisition"||kind=="host-package"`, 1)
	controlled = strings.Replace(controlled, `case "image":ref:=`,
		`case "image":if mode=="terminal barrier"{f,err:=os.OpenFile(filepath.Join(fixtureRoot,"terminal-image-calls"),os.O_CREATE|os.O_WRONLY|os.O_APPEND,0600);fail(err);_,err=f.WriteString("image\n");fail(err);fail(f.Close());fail(os.WriteFile(filepath.Join(fixtureRoot,"terminal-started"),nil,0600));for{if _,err:=os.Stat(filepath.Join(fixtureRoot,"release-terminal"));err==nil{break};time.Sleep(time.Millisecond)}};ref:=`, 1)
	controlled = strings.Replace(controlled, `if kind=="apt-acquisition"{`, `if kind=="apt-acquisition"||kind=="host-package"{`, 1)
	controlled = strings.Replace(controlled, `case "rm":for _,target`, `case "rm":if mode=="cleanup-failure"{os.Exit(7)};if mode=="close during scan"{if _,err:=os.Stat(filepath.Join(fixtureRoot,"scan-started"));err==nil{fail(os.WriteFile(filepath.Join(fixtureRoot,"cleanup-started"),nil,0600));for{if _,err:=os.Stat(filepath.Join(fixtureRoot,"release-cleanup"));err==nil{break};time.Sleep(time.Millisecond)}}};for _,target`, 1)
	controlled = strings.Replace(controlled, `mode=="cancel during root scan"`, `mode=="cancel during root scan"||mode=="close during scan"`, 1)
	controlled = strings.Replace(controlled, `files:=[]map[string]any{`, `if mode=="wrong scanner"{p[7]="sha256:"+strings.Repeat("f",64)};files:=[]map[string]any{`, 1)
	controlled = strings.Replace(controlled, `fail(json.NewEncoder(os.Stdout).Encode(map[string]any{"subject":digest`,
		`if mode=="oversized document"{files=nil;for i:=0;i<100000;i++{files=append(files,map[string]any{"path":"file-"+strings.Repeat("%",20)+fmt.Sprintf("-%06d",i),"kind":"regular","size":1,"digest":fmt.Sprintf("sha256:%x",sha256.Sum256([]byte("x")))})};fileReport,err:=json.Marshal(files);fail(err);fail(os.WriteFile(filepath.Join(fixtureRoot,"file-report-size"),[]byte(strconv.Itoa(len(fileReport))),0600))};fail(json.NewEncoder(os.Stdout).Encode(map[string]any{"subject":digest`, 1)
	if err := os.WriteFile(source, []byte(controlled), 0600); err != nil {
		t.Fatal(err)
	}
	inspectionPath := filepath.Join(inspectionRoot, "docker")
	command := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", "-X main.fixtureRoot="+inspectionRoot, "-o", inspectionPath, source)
	command.Env = append(os.Environ(), "CGO_ENABLED=0")
	if raw, err := command.CombinedOutput(); err != nil {
		t.Fatalf("controlled inspector ELF: %v %s", err, raw)
	}
	raw, err := os.ReadFile(inspectionPath)
	if err != nil {
		t.Fatal(err)
	}
	pin := pipeline.FileDigest(raw)
	profile := definition.DiskInspectionProfile{Image: "example.test/inspection@" + pin, InspectorDigest: pin, CodecDigest: pin, GuestfishDigest: pin, ScannerDigest: pin, PackageReaderDigest: pin,
		Resources: execution.ResourceRequest{CPUMillis: 1000, MemoryMiB: 9000, EphemeralStorageMiB: 4096}, TimeoutSeconds: 30, MaxFilesystemBytes: 1 << 20, ScratchStorage: "private-volume"}
	for name, value := range map[string]string{"mode": mode, "pin": pin} {
		if err := os.WriteFile(filepath.Join(inspectionRoot, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	repo := t.TempDir()
	debianBridgeGit(t, ctx, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "input.txt"), []byte("controlled pinned source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	debianBridgeGit(t, ctx, repo, "add", "input.txt")
	debianBridgeGit(t, ctx, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "controlled source fixture")
	commit := debianBridgeGit(t, ctx, repo, "rev-parse", "HEAD")
	tree := debianBridgeGit(t, ctx, repo, "rev-parse", "HEAD^{tree}")
	d, env, stack := debianFactoryInputs(t)
	component := stack.Catalog.Components["agent"]
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url."+filepath.ToSlash(repo)+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", component.Repo)
	component.Commit = commit
	component.TreeHash = tree
	stack, err = catalog.ApplyStack(&catalog.Catalog{Components: map[string]catalog.Component{"agent": component}})
	if err != nil {
		t.Fatal(err)
	}
	d.Outputs[0].Platform = "linux/" + runtime.GOARCH
	env.UtilityPlatform = d.Outputs[0].Platform
	env.Runtime.Runtime = "docker"
	env.DiskInspectionProfiles = map[string]definition.DiskInspectionProfile{d.Outputs[0].Platform: profile}
	plan, err := Compile(ctx, d, env, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	var fetch Node
	for _, node := range plan.Graph.Nodes {
		if node.Kind == NodeFetch {
			fetch = node
			break
		}
	}
	if err := executeProducer(ctx, plan, fetch, plan.Environment.RuntimeBinary(), 1, io.Discard, io.Discard, (pipeline.Local{}).ExecNode, nil); err != nil {
		t.Fatal("actual controlled fetch", err)
	}
	digest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	result := &LocalRunResult{SchemaVersion: LocalRunSchemaVersion, PlanDigest: digest, Qualification: TierLocalExecution}
	session, err := pipeline.NewNodeSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	t.Cleanup(func() { _ = result.Close() })
	if err := bindDebianRunSession(ctx, plan, result, session); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "docker")
	script := fmt.Sprintf(`#!/bin/sh
set -eu
case "$1" in
 version) printf '{"Client":{"Version":"1"},"Server":{"Version":"1"}}'; exit 0;;
 info) printf '"controlled-daemon"'; exit 0;;
 image) printf '{"Id":"sha256:%s","RepoDigests":["example.test/toolchain@sha256:%s"],"Os":"linux","Architecture":"%s","Config":{"Env":[],"Volumes":null}}'; exit 0;;
 rm) if [ %q = cleanup-failure ]; then exit 7; fi; exit 0;;
esac
out=
while [ "$#" -gt 0 ]; do
 case "$1" in
 -v) case "$2" in *:/src/out) out=${2%%:/src/out};; esac; shift 2;;
 *) shift;;
 esac
done
[ -n "$out" ]
printf 'controlled final archive' > "$out/agent-%s.deb"
`, strings.Repeat("a", 64), strings.Repeat("c", 64), runtime.GOARCH, "success", runtime.GOARCH)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inspectionRoot, "producer-runtime-path"), []byte(path), 0600); err != nil {
		t.Fatal(err)
	}
	if err := result.debian.executeBoundProducer(ctx, result, plan, plan.LifecycleOutputs[0], path, io.Discard, io.Discard); err != nil {
		t.Fatal("actual producer", err)
	}
	workspace, err := os.Open(inspectionRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	var inspector *sbom.DiskInspectionRuntime
	if mode == "different inspector profile" {
		inspector, err = sbom.OpenDiskInspectionRuntime(ctx, pipeline.DiskInspectionSelection{RuntimeExecutable: inspectionPath, Workspace: workspace, Image: profile.Image, Platform: "linux/" + runtime.GOARCH,
			InspectorDigest: profile.InspectorDigest, CodecDigest: profile.CodecDigest, GuestfishDigest: profile.GuestfishDigest, ScannerDigest: "sha256:" + strings.Repeat("e", 64), PackageReaderDigest: profile.PackageReaderDigest,
			Resources: profile.Resources, TimeoutSeconds: profile.TimeoutSeconds, MaxFilesystemBytes: profile.MaxFilesystemBytes, ScratchStorage: profile.ScratchStorage})
	} else {
		inspector, err = OpenDiskEvidenceRuntime(ctx, plan, inspectionPath, workspace)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = inspector.Close() })
	return plan, result, inspector, inspectionRoot
}

func TestDebianRunContentNativeJoin(t *testing.T) {
	if os.Getenv("NVKR_TEST_DEBIAN_CONTENT_RUNTIME") != "1" {
		t.Skip("explicit native inspected-content join requires real composition capacity")
	}
	for _, tc := range []struct {
		name string
		want e.Code
	}{
		{"issued", ""}, {"substituted report subject", e.CodeVerifyFailed}, {"wrong scanner", e.CodeVerifyFailed},
		{"cleanup-failure", e.CodeInternal}, {"closed inspector", e.CodeEngineCapability}, {"closed Run", e.CodeEngineCapability},
		{"changed profile", e.CodeVerifyFailed}, {"different inspector profile", e.CodeVerifyFailed}, {"decoded Run", e.CodeVerifyFailed}, {"sibling output", e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			plan, result, inspector, _ := debianRunContentFixture(t, ctx, tc.name)
			if tc.name == "closed inspector" {
				if err := inspector.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "closed Run" {
				if err := result.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "changed profile" {
				profile := plan.Environment.DiskInspectionProfiles["linux/"+runtime.GOARCH]
				profile.ScannerDigest = "sha256:" + strings.Repeat("e", 64)
				plan.Environment.DiskInspectionProfiles["linux/"+runtime.GOARCH] = profile
			}
			owner := result.debian
			originalResult := result
			output := plan.LifecycleOutputs[0]
			if tc.name == "decoded Run" {
				raw, err := canonical.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				var decoded LocalRunResult
				if err := canonical.UnmarshalExact(raw, &decoded); err != nil {
					t.Fatal(err)
				}
				originalResult = &decoded
			}
			if tc.name == "sibling output" {
				output.Name = "other-name"
			}
			document, err := owner.inspectBoundProducer(ctx, originalResult, plan, output, inspector)
			if tc.want != "" {
				if document != nil || factoryErrorCode(err) != tc.want || len(result.debian.content) != 0 {
					t.Fatal("rejected join retained content or wrong code", err)
				}
				return
			}
			if err != nil {
				t.Fatal("original inspected join", err)
			}
			completion := result.debian.completions[plan.LifecycleOutputs[0].ArtifactID]
			if document.Metadata.Component.Version != "1.0.0" {
				t.Fatal("recipe subject version changed", document.Metadata.Component)
			}
			found := false
			for _, component := range document.Components {
				if component.Name == "captured" {
					found = true
					if component.Version != "1" {
						t.Fatal("observed Debian Version changed")
					}
				}
			}
			if !found {
				t.Fatal("inspected package missing")
			}
			if len(document.Metadata.Component.Hashes) != 1 || document.Metadata.Component.Hashes[0].Content != strings.TrimPrefix(completion.description.Digest, "sha256:") {
				t.Fatal("archive subject changed")
			}
			stored := result.debian.content[plan.LifecycleOutputs[0].ArtifactID]
			document.Metadata.Component.Name = "caller substitution"
			var original sbom.BOM
			if err := canonical.UnmarshalExact(stored.bytes, &original); err != nil || original.Metadata.Component.Name != "agent" {
				t.Fatal("caller mutation changed stored content", err)
			}
			if got, err := result.debian.inspectBoundProducer(ctx, result, plan, plan.LifecycleOutputs[0], inspector); got != nil || factoryErrorCode(err) != e.CodeVerifyFailed {
				t.Fatal("duplicate content", err)
			}
			var retained bytes.Buffer
			if _, err := completion.archive.WriteToContext(ctx, &retained); err != nil || retained.String() != "controlled final archive" {
				t.Fatal("inspection changed producer bytes", err)
			}
		})
	}
}

func TestDebianRunContentDocumentLimitNative(t *testing.T) {
	if os.Getenv("NVKR_TEST_DEBIAN_CONTENT_RUNTIME") != "1" || os.Getenv("NVKR_TEST_DEBIAN_DOCUMENT_LIMIT") != "1" {
		t.Skip("explicit complete-document capacity regression")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	plan, result, inspector, root := debianRunContentFixture(t, ctx, "oversized document")
	document, err := result.debian.inspectBoundProducer(ctx, result, plan, plan.LifecycleOutputs[0], inspector)
	rawSize, sizeErr := os.ReadFile(filepath.Join(root, "file-report-size"))
	if sizeErr != nil {
		t.Fatal(sizeErr)
	}
	reportSize, sizeErr := strconv.Atoi(string(rawSize))
	if sizeErr != nil || reportSize >= pipeline.MaxDiskContentReportBytes-4096 {
		t.Fatal("fixture breached report budget", reportSize, sizeErr)
	}
	t.Logf("fixture file report=%d bytes; complete BOM must exceed %d bytes", reportSize, sbom.MaxDocumentBytes)
	if document != nil || factoryErrorCode(err) != e.CodeVerifyFailed || len(result.debian.content) != 0 {
		t.Fatal("oversized complete document retained or wrong code", err)
	}
	if err == nil || !strings.Contains(err.Error(), "complete SBOM exceeds") {
		t.Fatal("wrong limit rejection", err)
	}
}

// Close/cancellation must join the actual scan and checked cleanup before a
// document can escape. A borrowed inspector never owns the producer bytes.
func TestDebianRunContentCloseJoinsNativeScan(t *testing.T) {
	if os.Getenv("NVKR_TEST_DEBIAN_CONTENT_RUNTIME") != "1" {
		t.Skip("explicit native inspected-content join requires real composition capacity")
	}
	for _, closer := range []string{"Run", "inspector", "caller"} {
		t.Run(closer, func(t *testing.T) {
			barrierContext, barrierCancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer barrierCancel()
			ctx, cancel := context.WithCancel(barrierContext)
			defer cancel()
			plan, result, inspector, root := debianRunContentFixture(t, barrierContext, "close during scan")
			completion := result.debian.completions[plan.LifecycleOutputs[0].ArtifactID]
			scanDone := make(chan error, 1)
			closeDone := make(chan error, 1)
			scanJoined, closeJoined, closeStarted := false, false, false
			go func() {
				document, err := result.debian.inspectBoundProducer(ctx, result, plan, plan.LifecycleOutputs[0], inspector)
				if document != nil {
					err = e.New(e.CodeInternal, "closed scan returned a document")
				}
				scanDone <- err
			}()
			defer func() {
				cancel()
				_ = os.WriteFile(filepath.Join(root, "release-cleanup"), nil, 0600)
				if !scanJoined {
					<-scanDone
				}
				if closeStarted && !closeJoined {
					<-closeDone
				}
			}()
			waitMarker := func(marker string) {
				ticker := time.NewTicker(time.Millisecond)
				defer ticker.Stop()
				for {
					if _, err := os.Stat(filepath.Join(root, marker)); err == nil {
						return
					}
					select {
					case err := <-scanDone:
						scanJoined = true
						t.Fatal("scan ended before barrier", marker, err)
					case <-barrierContext.Done():
						t.Fatal("barrier timeout", marker, barrierContext.Err())
					case <-ticker.C:
					}
				}
			}
			waitMarker("scan-started")
			switch closer {
			case "Run":
				closeStarted = true
				go func() { closeDone <- result.Close() }()
			case "inspector":
				closeStarted = true
				go func() { closeDone <- inspector.Close() }()
			case "caller":
				cancel()
			}
			waitMarker("cleanup-started")
			if closeStarted {
				select {
				case err := <-closeDone:
					closeJoined = true
					t.Fatal("Close ended before checked cleanup", err)
				default:
				}
			}
			select {
			case err := <-scanDone:
				scanJoined = true
				t.Fatal("scan ended before checked cleanup", err)
			default:
			}
			if err := os.WriteFile(filepath.Join(root, "release-cleanup"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			scanErr := <-scanDone
			scanJoined = true
			if factoryErrorCode(scanErr) != e.CodeTimeout || len(result.debian.content) != 0 {
				t.Fatal("canceled scan retained content or wrong code", scanErr)
			}
			if closeStarted {
				closeErr := <-closeDone
				closeJoined = true
				if closeErr != nil {
					t.Fatal(closeErr)
				}
			}
			_, err := completion.archive.WriteToContext(t.Context(), io.Discard)
			if closer == "Run" {
				if factoryErrorCode(err) != e.CodeEngineCapability {
					t.Fatal("closed Run retained archive", err)
				}
			} else if err != nil {
				t.Fatal("borrowed inspector/caller revoked archive", err)
			}
		})
	}
}
