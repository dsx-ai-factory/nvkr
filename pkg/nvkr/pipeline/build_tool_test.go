// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestBuildToolBootstrapRefusesSubstitution(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux sealed descriptor bootstrap")
	}
	if _, err := os.Stat("/usr/bin/perl"); err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("required bootstrap interpreter is unavailable: %v", err)
		}
		t.Skip("Perl is unavailable")
	}
	raw := producedExecutableELFFixture()
	code := []byte{0xb8, 60, 0, 0, 0, 0x31, 0xff, 0x0f, 0x05} // exit(0), amd64
	if runtime.GOARCH == "arm64" {
		code = []byte{0xa8, 0x0b, 0x80, 0xd2, 0x00, 0x00, 0x80, 0xd2, 0x01, 0x00, 0x00, 0xd4}
	}
	binary.LittleEndian.PutUint64(raw[24:], 0x400000+uint64(len(raw)))
	raw = append(raw, code...)
	binary.LittleEndian.PutUint64(raw[96:], uint64(len(raw)))
	binary.LittleEndian.PutUint64(raw[104:], uint64(len(raw)))
	for _, test := range []struct {
		name     string
		change   func(*[]byte, *string, *string)
		succeeds bool
	}{
		{"two invocations", func(*[]byte, *string, *string) {}, true},
		{"changed bytes", func(b *[]byte, _ *string, _ *string) { (*b)[len(*b)-1] ^= 1 }, false},
		{"truncated", func(b *[]byte, _ *string, _ *string) { *b = (*b)[:len(*b)-1] }, false},
		{"extra bytes", func(b *[]byte, _ *string, _ *string) { *b = append(*b, 0) }, false},
		{"wrong architecture", func(_ *[]byte, p *string, _ *string) { *p = "linux/386" }, false},
		{"unbounded", func(_ *[]byte, _ *string, size *string) { *size = "536870913" }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			input, platform, size := bytes.Clone(raw), "linux/"+runtime.GOARCH, strconv.Itoa(len(raw))
			test.change(&input, &platform, &size)
			marker := filepath.Join(t.TempDir(), "command-started")
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/usr/bin/perl", "-f", "-e", buildToolBootstrap, "--", platform, FileDigest(raw), size,
				"/bin/sh", "-ec", `[ "$(readlink /proc/self/fd/0)" = /dev/null ]; [ -z "$(cat)" ]; "$1"; "$1"; touch "$2"`, "--", ir.BuildToolPath, marker)
			cmd.Stdin = bytes.NewReader(input)
			output, err := cmd.CombinedOutput()
			if (err == nil) != test.succeeds {
				t.Fatalf("bootstrap: %v: %s", err, output)
			}
			_, statErr := os.Stat(marker)
			if (statErr == nil) != test.succeeds {
				t.Fatalf("source command admission differs: %v", statErr)
			}
		})
	}
}

func TestBuildToolHeldBytesAndAdmission(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("held executable admission requires Linux")
	}
	raw := producedExecutableELFFixture()
	name := filepath.Join(t.TempDir(), "tool")
	if err := os.WriteFile(name, raw, 0o500); err != nil {
		t.Fatal(err)
	}
	node := ir.Node{ID: "build-consumer", Platform: "linux/" + runtime.GOARCH,
		BuildTool: &ir.BuildToolBinding{Name: "tool", ExecutableDigest: FileDigest(raw)}}
	opts := RunOptions{Ctx: t.Context(), Launch: true, ImageAccess: ImagePreparedOnly, ToolExecutable: name}
	file, err := captureBuildTool(node, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err := os.Chmod(name, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte("changed"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := file.AssertUnchangedContext(t.Context()); err != nil {
		t.Fatalf("mutable path changed the held executable: %v", err)
	}
	for _, test := range []struct {
		name   string
		change func(*ir.Node, *RunOptions)
		code   e.Code
	}{
		{"changed input", func(*ir.Node, *RunOptions) {}, e.CodeVerifyFailed},
		{"missing input", func(_ *ir.Node, o *RunOptions) { o.ToolExecutable = name + "-absent" }, e.CodeVerifyFailed},
		{"undeclared input", func(_ *ir.Node, o *RunOptions) { o.ToolExecutable = "" }, e.CodeProvenanceMissing},
		{"wrong architecture", func(n *ir.Node, _ *RunOptions) { n.Platform = "linux/386" }, e.CodeEngineCapability},
		{"no image admission", func(_ *ir.Node, o *RunOptions) { o.ImageAccess = "" }, e.CodeEngineCapability},
		{"not launched", func(_ *ir.Node, o *RunOptions) { o.Launch = false }, e.CodeEngineCapability},
		{"canceled", func(_ *ir.Node, o *RunOptions) { ctx, cancel := context.WithCancel(t.Context()); cancel(); o.Ctx = ctx }, e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			n, o := node, opts
			test.change(&n, &o)
			got, err := captureBuildTool(n, o)
			if got != nil {
				_ = got.Close()
				t.Fatal("invalid input returned a capability")
			}
			if pinnedToolCode(err) != test.code {
				t.Fatalf("got %v; want %s", err, test.code)
			}
		})
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.AssertUnchangedContext(t.Context()); err == nil {
		t.Fatal("closed executable remained usable")
	}
}

func TestBuildToolResultRequiresExactObservation(t *testing.T) {
	digest := FileDigest([]byte("pinned tool"))
	for _, test := range []struct {
		name     string
		binding  bool
		observed string
		valid    bool
	}{
		{"exact", true, digest, true},
		{"missing", true, "", false},
		{"changed", true, FileDigest([]byte("other tool")), false},
		{"undeclared", false, digest, false},
		{"ordinary source", false, "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			node := ir.Node{ID: "consumer", Kind: ir.KindBuild, BuilderType: ir.BuilderOCIHermetic}
			if test.binding {
				node.BuildTool = &ir.BuildToolBinding{ExecutableDigest: digest}
			}
			result := ArtifactResult{InputDigest: FileDigest([]byte("source")), Qualification: ArtifactUnqualified,
				BuildToolDigest: test.observed}
			_, err := authenticateConsumedResultMetadata(node, result)
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else if pinnedToolCode(err) != e.CodeVerifyFailed {
				t.Fatalf("got %v; want %s", err, e.CodeVerifyFailed)
			}
			payload := filepath.Join(t.TempDir(), "payload")
			raw := []byte("output bytes")
			if err := os.WriteFile(payload, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			result.SHA256, result.Size, result.BuilderType = FileDigest(raw), int64(len(raw)), node.BuilderType
			record := payload + ".json"
			writeObservedResult(t, record, result)
			_, err = ObserveResult(t.Context(), node, payload, record)
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else if pinnedToolCode(err) != e.CodeVerifyFailed {
				t.Fatalf("observation: got %v; want %s", err, e.CodeVerifyFailed)
			}
		})
	}
}
