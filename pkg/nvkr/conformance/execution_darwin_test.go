// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build darwin

package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testresults"
)

func TestLocalDarwinExecutionOwnsSnapshotsAndCancellation(t *testing.T) {
	if runtime.GOARCH != "arm64" {
		t.Skip("native ARM64 diagnostic owner")
	}
	root := t.TempDir()
	source := filepath.Join(root, "main.go")
	program := `package main
import("os";"strings";"time")
func main(){
 p:=strings.TrimPrefix(os.Args[1],"--kubeconfig=")
 b,e:=os.ReadFile(p); if e!=nil || string(b)!="synthetic-config" { os.Exit(2) }
 if os.Getenv("NVKR_CONFORMANCE_PARENT_ONLY")!="" { os.Exit(3) }
 switch os.Args[2] {case "sleep":time.Sleep(10*time.Second);case "tamper":p,_:=os.Executable();_ = os.Chmod(p,0600);case "tamper-exit1":p,_:=os.Executable();_ = os.Chmod(p,0600);os.Exit(1);case "exit1":os.Exit(1)}
 os.Stdout.WriteString("original-result")
}`
	if err := os.WriteFile(source, []byte(program), 0600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "fixture")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", binary, source)
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build native fixture: %v: %s", err, out)
	}
	raw, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal(err)
	}
	tool, err := heldfile.FromBytesContext(t.Context(), raw, heldfile.Options{Label: "native fixture", MaxBytes: maxToolBytes})
	if err != nil {
		t.Fatal(err)
	}
	defer tool.Close()
	kube, err := heldfile.FromBytesContext(t.Context(), []byte("synthetic-config"), heldfile.Options{Label: "private fixture config", MaxBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	defer kube.Close()
	t.Setenv("NVKR_CONFORMANCE_PARENT_ONLY", "must-not-inherit")
	for _, mode := range []string{"exit1", "tamper-exit1"} {
		t.Run("command-"+mode, func(t *testing.T) {
			owner := &admitted{hydrophone: tool, kubeBytes: []byte("synthetic-config"), options: Options{localDiagnostic: true, Kubeconfig: Tool{Digest: blobDigest([]byte("synthetic-config"))}}}
			work := t.TempDir()
			// Exercise exit classification under the normal local command budget;
			// cancellation timing is tested separately below.
			_, code, err := owner.command(t.Context(), tool, []string{mode}, work, nil, time.Minute)
			if code != 1 || (err != nil) != (mode == "tamper-exit1") {
				t.Fatalf("joined failure misclassified: code=%d error=%v", code, err)
			}
			owner.hydrophone = nil // The fixture shares this held input across cases.
			owner.close()
			if err := owner.closeNative(); err != nil {
				t.Fatal(err)
			}
		})
	}

	for _, mode := range []string{"success", "tamper", "sleep"} {
		t.Run(mode, func(t *testing.T) {
			work := t.TempDir()
			var stdout bytes.Buffer
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if mode == "sleep" {
				timer := time.AfterFunc(250*time.Millisecond, cancel)
				defer timer.Stop()
			}
			start := time.Now()
			owner := &admitted{}
			err := owner.runLocalNativeCommand(ctx, tool, []string{"--kubeconfig=/dev/fd/4", mode}, heldfile.RunOptions{Dir: work, Env: []string{"PATH=", "KUBECONFIG=/dev/fd/4"}, Stdout: &stdout}, kube)
			switch mode {
			case "success":
				if err != nil || stdout.String() != "original-result" {
					t.Fatalf("result %q: %v", stdout.String(), err)
				}
			case "tamper":
				requireCode(t, err, e.CodeVerifyFailed)
			case "sleep":
				if !errors.Is(err, context.Canceled) || time.Since(start) > 3*time.Second {
					t.Fatalf("cancellation not joined: %v", err)
				}
			}
			owner.close()
			if err := owner.closeNative(); err != nil {
				t.Fatal(err)
			}
			entries, readErr := os.ReadDir(work)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("native command snapshots retained: %v %v", entries, readErr)
			}
		})
	}
}

func TestDarwinExecutablesAreLocalDiagnosticOnly(t *testing.T) {
	selector := ExecutableSelector{Output: "tools", Member: "hydrophone", Platform: "darwin/arm64"}
	requireCode(t, validateExecutableSelectorFor(selector, false), e.CodeEngineCapability)
	if err := validateExecutableSelectorFor(selector, true); err != nil {
		t.Fatal(err)
	}
	selector.Platform = "linux/arm64"
	requireCode(t, validateExecutableSelectorFor(selector, true), e.CodeEngineCapability)
	// Global held execution remains unavailable, including signing boundaries.
	if heldfile.RequireExecutableSupport() == nil {
		t.Fatal("global Darwin held execution was widened")
	}
}

// Run the existing shared Hydrophone/API process fixture through the Mac adapter,
// including original report retention and the normal nonzero harness case.
func TestDarwinConformanceRetainsOriginalReport(t *testing.T) {
	if runtime.GOARCH != "arm64" {
		t.Skip("native ARM64 diagnostic owner")
	}
	for _, mode := range []string{"success", "failed"} {
		t.Run(mode, func(t *testing.T) {
			root, resolveErr := filepath.EvalSymlinks(t.TempDir())
			if resolveErr != nil {
				t.Fatal(resolveErr)
			}
			state := filepath.Join(root, "state.json")
			bin := filepath.Join(root, "tool")
			command := exec.CommandContext(t.Context(), "go", "build", "-buildvcs=false", "-trimpath", "-ldflags=-X main.mode="+mode+" -X main.state="+state, "-o", bin, "./testdata/tool.go")
			command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOTOOLCHAIN=local")
			if raw, err := command.CombinedOutput(); err != nil {
				t.Fatalf("build shared native fixture: %v %s", err, raw)
			}
			kube := filepath.Join(root, "kubeconfig")
			if err := os.WriteFile(kube, []byte(kubeFixture), 0600); err != nil {
				t.Fatal(err)
			}
			options := localOptionsFixture()
			// Report retention uses the normal local command budget; cancellation
			// timing is covered independently by the native ownership test.
			options.CommandTimeout = time.Minute
			options.localDiagnostic = true
			options.WorkspaceRoot = root
			options.Kubeconfig = Tool{Path: kube, Digest: blobDigest([]byte(kubeFixture))}
			tools := toolsDescriptionFixture()
			tools.Outputs[0].Platform = "darwin/arm64"
			producer := &Producer{options: options, captureTool: fixtureCapture(options)}
			output, err := producer.produce(t.Context(), artifactDescriptionFixture(), tools)
			if err != nil {
				t.Fatal(err)
			}
			if output.CaptureStatus != testresults.CaptureComplete || len(output.Reports) != 1 {
				t.Fatalf("incomplete capture: %+v", output)
			}
			report, err := os.ReadFile(output.Reports[0].Reference)
			if err != nil || blobDigest(report) != output.Reports[0].Digest {
				t.Fatalf("original report changed: %v", err)
			}
			if (strings.Contains(string(report), `failures="1"`)) != (mode == "failed") {
				t.Fatalf("original verdict changed: %s", report)
			}
			// The normal Hydrophone fixture removes its own objects. The owner
			// observes their absence instead of issuing an unnecessary delete.
			stateBytes, err := os.ReadFile(state)
			var remaining map[string]string
			if err != nil || json.Unmarshal(stateBytes, &remaining) != nil || len(remaining) != 0 {
				t.Fatalf("shared suite cleanup incomplete: %v", err)
			}
		})
	}
}
