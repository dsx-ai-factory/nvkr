// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// This fake runtime observes the full launched executor boundary. It does not
// execute Perl or establish container isolation; the bootstrap has separate tests.
func TestBuildToolExecutorWiring(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed=%t", changed), func(t *testing.T) {
			root := t.TempDir()
			runtimePath, image, runtimeRoot := imageAdmissionRuntimeFixture(t)
			imageJSON := fmt.Sprintf(`{"Id":%q,"RepoDigests":[%q],"Os":"linux","Architecture":%q}`,
				"sha256:"+strings.Repeat("b", 64), image, runtime.GOARCH)
			if err := os.WriteFile(filepath.Join(runtimeRoot, "image"), []byte(imageJSON), 0o600); err != nil {
				t.Fatal(err)
			}
			daemonJSON := fmt.Sprintf(`{"ID":"daemon-a","OSType":"linux","Architecture":%q}`, runtime.GOARCH)
			if err := os.WriteFile(filepath.Join(runtimeRoot, "daemon"), []byte(daemonJSON), 0o600); err != nil {
				t.Fatal(err)
			}
			argvPath, stdinPath := filepath.Join(root, "argv"), filepath.Join(root, "stdin")
			script, err := os.ReadFile(runtimePath)
			if err != nil {
				t.Fatal(err)
			}
			body := fmt.Sprintf(`run)
printf '%%s\000' "$@" > %q
cat > %q
out=
for a in "$@"; do case "$a" in *:/src/out) out=${a%%:/src/out};; esac; done
[ -n "$out" ] || exit 91
printf artifact > "$out/package.deb"
;;`, argvPath, stdinPath)
			script = bytes.Replace(script, []byte(`run|create) case " $* " in *' --pull=never '*) exit 0;; *) exit 93;; esac;;`), []byte(body), 1)
			if err := os.WriteFile(runtimePath, script, 0o755); err != nil {
				t.Fatal(err)
			}
			sourceRoot := filepath.Join(root, "src")
			source := filepath.Join(sourceRoot, "repo")
			if err := os.MkdirAll(source, 0o755); err != nil {
				t.Fatal(err)
			}
			fetch := ir.Node{ID: "fetch-repo", Dest: "repo", Commit: goodSourceCommit, TreeHash: goodSourceCommit}
			vendorDigest, err := fetchVendorSpecDigest(fetch)
			if err != nil {
				t.Fatal(err)
			}
			closure, err := sourceTreeDigestContext(t.Context(), source)
			if err != nil {
				t.Fatal(err)
			}
			receiptPath, err := sourceReceiptPath(sourceRoot, fetch.Dest)
			if err != nil {
				t.Fatal(err)
			}
			if err := writeSourceReceiptContext(t.Context(), receiptPath, sourceReceipt{
				Schema: sourceReceiptSchema, Commit: fetch.Commit, TreeHash: fetch.TreeHash,
				VendorSpecDigest: vendorDigest, ClosureDigest: closure,
			}); err != nil {
				t.Fatal(err)
			}
			raw := producedExecutableELFFixture()
			digest := FileDigest(raw)
			toolPath := filepath.Join(root, "tool")
			input := bytes.Clone(raw)
			if changed {
				input[len(input)-1] ^= 1
			}
			if err := os.WriteFile(toolPath, input, 0o500); err != nil {
				t.Fatal(err)
			}
			platform := "linux/" + runtime.GOARCH
			node := ir.Node{ID: "build-package", Kind: ir.KindBuild, Component: "package", Needs: []string{fetch.ID},
				BuilderType: ir.BuilderOCIHermetic, Platform: platform, Network: ir.NetworkNone,
				Toolchain: image, Workdir: "/src", WriteDir: "out", ArtifactPath: "out/package.deb", OutputType: "host-package", Version: "1.0.0",
				SourceDest: fetch.Dest, SourceCommit: fetch.Commit, SourceTreeHash: fetch.TreeHash,
				VendorSpecDigest: vendorDigest, SourceReadOnly: true, Env: testBuildEnv(runtime.GOARCH),
				Command: []string{"make", "NFPM_BIN=" + ir.BuildToolPath}, BuildToolURL: "https://example.test/tool",
				BuildTool: &ir.BuildToolBinding{Name: "nfpm", Platform: platform, Format: "raw", Member: "nfpm",
					ArchiveDigest: digest, ExecutableDigest: digest}}
			cleared := []string{"PERL5OPT", "PERL5LIB", "PERLLIB", "LD_PRELOAD", "LD_LIBRARY_PATH"}
			result, err := (Local{}).ExecNode(node, RunOptions{Ctx: t.Context(), Launch: true,
				ImageAccess: ImagePreparedOnly, Runtime: runtimePath, SourceRoot: sourceRoot,
				ToolExecutable: toolPath, Stdout: io.Discard, Stderr: io.Discard})
			if changed {
				if result != nil || pinnedToolCode(err) != e.CodeVerifyFailed {
					t.Fatalf("changed executable: result=%v err=%v", result, err)
				}
				if _, err := os.Stat(filepath.Join(runtimeRoot, "calls")); !os.IsNotExist(err) {
					t.Fatalf("runtime invoked before tool authentication: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.BuildToolDigest != digest || result.SHA256 != FileDigest([]byte("artifact")) {
				t.Fatalf("wrong observed input/output: %+v", result)
			}
			stdin, err := os.ReadFile(stdinPath)
			if err != nil || !bytes.Equal(stdin, raw) {
				t.Fatalf("runtime did not receive exact held bytes: %v", err)
			}
			argv, err := os.ReadFile(argvPath)
			if err != nil {
				t.Fatal(err)
			}
			args := strings.Split(strings.TrimSuffix(string(argv), "\x00"), "\x00")
			imageIndex := slices.Index(args, image)
			interactive, entrypoint := slices.Index(args, "--interactive"), slices.Index(args, "--entrypoint")
			if interactive < 0 || entrypoint <= interactive || entrypoint+1 >= imageIndex || args[entrypoint+1] != "/usr/bin/perl" {
				t.Fatalf("missing or misplaced interpreter launch flags: %q", args)
			}
			want := append([]string{"-f", "-e", buildToolBootstrap, "--", platform, digest, strconv.Itoa(len(raw))}, node.Command...)
			if !slices.Equal(args[imageIndex+1:], want) {
				t.Fatal("bootstrap arguments do not precede the exact source command")
			}
			for _, name := range cleared {
				if slices.Index(args, name+"=") <= slices.Index(args, "TZ=UTC") {
					t.Errorf("bootstrap reset %s must follow the authored environment", name)
				}
				last := "missing"
				for i := 0; i+1 < imageIndex; i++ {
					if args[i] == "-e" && strings.HasPrefix(args[i+1], name+"=") {
						last = args[i+1]
					}
				}
				if last != name+"=" {
					t.Errorf("bootstrap environment %s was not cleared last", name)
				}
			}
		})
	}
}
