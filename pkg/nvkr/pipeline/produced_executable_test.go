// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func producedExecutableGraph(platform, pin string) *ir.Graph {
	return &ir.Graph{SchemaVersion: ir.SchemaVersion, Platform: platform, Nodes: []ir.Node{
		{ID: "pull-helper", Kind: ir.KindPull, Component: "helper", OutputType: "host-package", Network: ir.NetworkDefault,
			Platform: platform, Artifact: "https://example.test/helper", Digest: pin, ArtifactPath: "helper", Version: "1.0.0",
			Env: []ir.EnvVar{{Name: "SOURCE_DATE_EPOCH", Value: "1"}}},
		{ID: "verify", Kind: ir.KindVerify, Needs: []string{"pull-helper"}},
	}}
}

func TestProducedExecutableAdmissionBeforePaths(t *testing.T) {
	platform := "linux/amd64"
	if runtime.GOARCH == "amd64" {
		platform = "linux/arm64"
	}
	graph := producedExecutableGraph(platform, "sha256:"+strings.Repeat("a", 64))
	for _, tc := range []struct {
		name, digest string
		code         e.Code
	}{
		{"wrong Build", "sha256:" + strings.Repeat("b", 64), e.CodeVerifyFailed},
		{"wrong architecture", mustGraphDigest(t, graph), e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, tool, err := (Local{}).ExecuteProducedExecutable(graph, tc.digest, "helper", RunOptions{Ctx: t.Context()})
			if tool != nil || code(err) != tc.code {
				t.Fatalf("tool=%v error=%v, want %s", tool, err, tc.code)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := (Local{}).ExecuteProducedExecutable(graph, mustGraphDigest(t, graph), "helper", RunOptions{Ctx: ctx}); code(err) != e.CodeTimeout {
		t.Fatalf("cancellation: %v", err)
	}
	var tool ProducedExecutable
	if _, err := tool.BindingContext(t.Context()); code(err) != e.CodeVerifyFailed {
		t.Fatalf("zero receipt: %v", err)
	}
}

// This is a format fixture, not a runnable inspector or operational evidence.
func producedExecutableELFFixture() []byte {
	raw := make([]byte, 120)
	copy(raw, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(raw[16:], 2)
	machine := uint16(62)
	if runtime.GOARCH == "arm64" {
		machine = 183
	}
	binary.LittleEndian.PutUint16(raw[18:], machine)
	binary.LittleEndian.PutUint32(raw[20:], 1)
	binary.LittleEndian.PutUint64(raw[24:], 0x400000)
	binary.LittleEndian.PutUint64(raw[32:], 64)
	binary.LittleEndian.PutUint16(raw[52:], 64)
	binary.LittleEndian.PutUint16(raw[54:], 56)
	binary.LittleEndian.PutUint16(raw[56:], 1)
	binary.LittleEndian.PutUint16(raw[58:], 64)
	binary.LittleEndian.PutUint32(raw[64:], 1)
	binary.LittleEndian.PutUint32(raw[68:], 5)
	binary.LittleEndian.PutUint64(raw[80:], 0x400000)
	binary.LittleEndian.PutUint64(raw[96:], uint64(len(raw)))
	binary.LittleEndian.PutUint64(raw[104:], uint64(len(raw)))
	return raw
}

func TestProducedExecutableAuthenticatesAndRevokes(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("executable capabilities require Linux")
	}
	for _, variant := range []string{"valid", "changed", "missing", "symlink", "wrong ELF architecture", "dynamic interpreter"} {
		t.Run(variant, func(t *testing.T) {
			raw := producedExecutableELFFixture()
			if variant == "wrong ELF architecture" {
				binary.LittleEndian.PutUint16(raw[18:], 3)
			}
			if variant == "dynamic interpreter" {
				binary.LittleEndian.PutUint32(raw[64:], 3)
			}
			graph := producedExecutableGraph("linux/"+runtime.GOARCH, FileDigest(raw))
			root := t.TempDir()
			payload := filepath.Join(root, "fetch", "helper", "helper")
			if err := os.MkdirAll(filepath.Dir(payload), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(payload, raw, 0o700); err != nil {
				t.Fatal(err)
			}
			result := ArtifactResult{Component: "helper", OutputType: "host-package", SHA256: FileDigest(raw), Size: int64(len(raw)),
				Qualification: ArtifactProfileFreeAcquisition, EngineClass: ir.EnginePrebuiltAcquire, Path: "helper"}
			writeResultForTest(t, root, "pull-helper", result)
			switch variant {
			case "changed":
				if err := os.WriteFile(payload, []byte("changed"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "missing":
				payload += "-absent"
			case "symlink":
				link := payload + "-link"
				if err := os.Symlink(payload, link); err != nil {
					t.Fatal(err)
				}
				payload = link
			}
			tool, err := captureProducedExecutableFixture(t.Context(), graph, mustGraphDigest(t, graph), payload, filepath.Join(root, "pull-helper.json"))
			if variant != "valid" {
				want := e.CodeVerifyFailed
				if variant == "wrong ELF architecture" || variant == "dynamic interpreter" {
					want = e.CodeEngineCapability
				}
				if tool != nil || code(err) != want {
					t.Fatalf("tool=%v error=%v, want %s", tool, err, want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer tool.Close()
			binding, err := tool.BindingContext(t.Context())
			if err != nil || binding.BuildDigest != mustGraphDigest(t, graph) || binding.Digest != result.SHA256 || binding.Component != "helper" {
				t.Fatalf("binding=%+v error=%v", binding, err)
			}
			copy := *tool
			if err := tool.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := copy.BindingContext(t.Context()); code(err) != e.CodeVerifyFailed {
				t.Fatalf("copy survived revocation: %v", err)
			}
		})
	}
}

// Unit fixtures exercise private capture separately from production issuance.
// No production API can turn these caller-written result files into a receipt.
func captureProducedExecutableFixture(ctx context.Context, graph *ir.Graph, digest, payload, resultPath string) (*ProducedExecutable, error) {
	producer, err := admitProducedExecutable(ctx, graph, digest, "helper")
	if err != nil {
		return nil, err
	}
	result, err := AuthenticateResultForConsumption(ctx, *producer, payload, resultPath)
	if err != nil {
		return nil, err
	}
	return captureExecutedTool(ctx, graph.Platform, digest, *producer, result, RunOptions{Ctx: ctx, SourceRoot: filepath.Join(filepath.Dir(resultPath), "src")})
}

func TestProducedExecutableConcreteAcquisition(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("executable capabilities require Linux")
	}
	raw := producedExecutableELFFixture()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(raw) }))
	defer server.Close()
	graph := producedExecutableGraph("linux/"+runtime.GOARCH, FileDigest(raw))
	graph.Nodes[0].Artifact = server.URL + "/helper"
	result, tool, err := (Local{}).ExecuteProducedExecutable(graph, mustGraphDigest(t, graph), "helper",
		RunOptions{Ctx: t.Context(), Launch: true, SourceRoot: filepath.Join(t.TempDir(), "src")})
	if err != nil {
		t.Fatal(err)
	}
	defer tool.Close()
	binding, err := tool.BindingContext(t.Context())
	if err != nil || binding.Digest != result.SHA256 || binding.Component != result.Component {
		t.Fatalf("binding=%+v result=%+v error=%v", binding, result, err)
	}
	if binding.TerminalDigest == "" {
		t.Fatal("tool lacks exact terminal receipt binding")
	}
	for _, tc := range []struct {
		name   string
		change func(*ArtifactResult)
	}{
		{"exact", func(*ArtifactResult) {}},
		{"engine claim", func(r *ArtifactResult) { r.EngineClass = "changed" }},
		{"size", func(r *ArtifactResult) { r.Size++ }},
		{"recorded path", func(r *ArtifactResult) { r.Path = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observed := *result
			tc.change(&observed)
			err := tool.AuthenticateResultContext(t.Context(), observed)
			if tc.name == "exact" {
				if err != nil {
					t.Fatal(err)
				}
			} else if code(err) != e.CodeVerifyFailed {
				t.Fatalf("changed metadata accepted: %v", err)
			}
		})
	}
	// A pathname replaced with equal bytes cannot recreate the original producer
	// inode. The held snapshot remains intact, but its live receipt fails closed.
	if err := os.Rename(result.AbsPath, result.AbsPath+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(result.AbsPath, raw, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := tool.BindingContext(t.Context()); code(err) != e.CodeVerifyFailed {
		t.Fatalf("path replacement survived terminal binding: %v", err)
	}
}
