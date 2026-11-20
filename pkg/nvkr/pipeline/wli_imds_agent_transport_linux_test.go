// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/sourcegit"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// The upstream NVIDIA Workload Identity v1.15.0 root module records two pinned
// third-party gitlinks while never initializing them for the agent build. This
// fixture reproduces that exact shape with a local Git repository so the held
// hosted transport is exercised against real Git objects and real gitlink trees.
const wliFixtureGitmodules = `[submodule "third-party/ovn-kubernetes"]
	path = third-party/ovn-kubernetes
	url = https://github.com/ovn-org/ovn-kubernetes.git
[submodule "third-party/doca-platform-foundation"]
	path = third-party/doca-platform-foundation
	url = https://github.com/NVIDIA/doca-platform.git
	branch = public-release-v26.8
`

var wliFixtureGitlinkDirectories = []string{
	"third-party/doca-platform-foundation",
	"third-party/ovn-kubernetes",
}

// wliSourceFixture builds the local repository and returns the pinned fetch node
// for it. The two gitlink object IDs are synthetic local tree members; they are
// never resolved or initialized.
func wliSourceFixture(t *testing.T, dest string) ir.Node {
	t.Helper()
	repository := filepath.Join(t.TempDir(), "repository")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		".gitmodules": wliFixtureGitmodules,
		"go.mod":      "module dsx-identity\n\ngo 1.26.0\n",
		"go.sum":      "",
		"cmd/dsx-imds-node-attestor-agent/main.go": "package main\n",
		"internal/dsximds/agent.go":                "package dsximds\n",
	}
	for name, body := range files {
		path := filepath.Join(repository, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The fixture Git children use the admitted source-Git baseline so they
	// neither inherit caller Git configuration nor clear parent policy; the
	// synthetic gitlinks are SHA-1 object IDs, so init pins that format exactly.
	environment, err := sourcegit.Baseline(os.Getenv("PATH"))
	if err != nil {
		t.Fatal(err)
	}
	git := func(arguments ...string) {
		t.Helper()
		command := exec.CommandContext(t.Context(), "git", append([]string{"-C", repository}, arguments...)...)
		command.Env = environment
		if raw, err := command.CombinedOutput(); err != nil {
			t.Fatalf("fixture git %v: %s, %v", arguments, raw, err)
		}
	}
	git("init", "-q", "--object-format=sha1")
	git("add", ".")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "commit", "-qm", "root module")
	// Gitlink entries are committed directly against synthetic submodule head
	// commits; the submodule worktrees do not exist in this fixture.
	git("update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("3", 40)+",third-party/doca-platform-foundation")
	git("update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("4", 40)+",third-party/ovn-kubernetes")
	git("-c", "user.name=Fixture", "-c", "user.email=fixture@example.test", "-c", "commit.gpgsign=false", "commit", "-qm", "pinned gitlinks")
	// Resolve the fixture identity with direct Git execution: the pipeline's
	// ordinary gitOut diagnostic refuses .gitmodules sources, and this fixture
	// must not bypass that refusal.
	resolve := func(revision string) string {
		t.Helper()
		command := exec.CommandContext(t.Context(), "git", "-C", repository, "rev-parse", revision)
		command.Env = environment
		out, err := command.Output()
		if err != nil {
			t.Fatalf("fixture git rev-parse %s: %v", revision, err)
		}
		return strings.TrimSpace(string(out))
	}
	commit := resolve("HEAD")
	tree := resolve("HEAD^{tree}")
	vendorSpec, err := ir.VendorSpecIdentity(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	return ir.Node{
		ID: "fetch-nvidia-workload-identity", Kind: ir.KindFetch, Repo: repository,
		Commit: commit, TreeHash: tree, Dest: dest, VendorSpecDigest: vendorSpec,
		Network: ir.NetworkDefault,
	}
}

// wliVendorCarrier renders the bounded return carrier a source-vendor container
// would produce for this selection: the vendored module manifest beside the
// retained root module files, .gitmodules, and both uninitialized gitlink
// directories. It is synthetic fixture output, not upstream dependency content.
func wliVendorCarrier(t *testing.T) []byte {
	t.Helper()
	type carrierEntry struct {
		name  string
		body  []byte
		isDir bool
	}
	entries := []carrierEntry{
		{name: "source/.gitmodules", body: []byte(wliFixtureGitmodules)},
		{name: "source/go.mod", body: []byte("module dsx-identity\n\ngo 1.26.0\n")},
		{name: "source/go.sum", body: []byte("")},
		{name: "source/cmd/dsx-imds-node-attestor-agent/main.go", body: []byte("package main\n")},
		{name: "source/internal/dsximds/agent.go", body: []byte("package dsximds\n")},
		{name: "source/vendor/modules.txt", body: []byte("# dsx-identity v1.15.0 fixture vendor manifest\n")},
	}
	for _, directory := range wliFixtureGitlinkDirectories {
		entries = append(entries, carrierEntry{name: "source/" + directory, isDir: true})
	}
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	for _, entry := range entries {
		header := &tar.Header{Name: entry.name, Mode: 0o644, Size: int64(len(entry.body))}
		if entry.isDir {
			header.Typeflag = tar.TypeDir
			header.Size = 0
		}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if len(entry.body) != 0 {
			if _, err := writer.Write(entry.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

// Load exact argv operands, including the YAML block's final LF, from the
// authored opt-in catalog. The fixture models transport, not a Go build.
func wliCatalogCommands(t *testing.T) (vendor, build []string) {
	t.Helper()
	cat, err := catalog.LoadContext(t.Context(), catalog.Dir("../../../inputs/catalogs/nvidia-workload-identity"))
	if err != nil {
		t.Fatal(err)
	}
	return slices.Clone(cat.Sources["nvidia-workload-identity"].VendorCommand), slices.Clone(cat.Components["dsx-imds-node-attestor-agent"].BuildCommand)
}

// wliAgentBuildNode mirrors the compiled producer declaration for the materializer.
func wliAgentBuildNode(t *testing.T, fetch ir.Node, command []string) ir.Node {
	t.Helper()
	build := completeTestBuild(ir.Node{
		ID: "build-dsx-imds-node-attestor-agent", Kind: ir.KindBuild,
		Component: "dsx-imds-node-attestor-agent",
	}, fetch)
	build.WriteDir = "nvkr-output"
	build.ArtifactPath = "nvkr-output/dsx-imds-node-attestor-agent"
	build.OutputType = "host-package"
	build.Command = slices.Clone(command)
	if err := build.Validate(); err != nil {
		t.Fatalf("fixture build node: %v", err)
	}
	return build
}

// The hosted source transport preserves the complete upstream root closure:
// held-workspace Git acquisition of a tree with a retained .gitmodules and two
// uninitialized gitlink directories, an owner-issued receipt, the vendor
// archive handoff with copy-back, and authenticated source materialization for
// the agent build. The modeled vendor daemon supplies synthetic closure bytes;
// this is contract evidence for the transport owners, not a public WLI network
// build, and no Local diagnostic refusal is bypassed.
func TestWLIIMDSAgentHeldSourceTransportMaterialization(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("actual held source transport requires Linux")
	}
	vendorCommand, buildCommand := wliCatalogCommands(t)
	t.Chdir(t.TempDir())
	workspace, err := HoldSourceWorkspace(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer workspace.Close()
	fetch := wliSourceFixture(t, "nvidia-workload-identity")

	// Phase 1: plain held Git acquisition issues its receipt over the pristine
	// closure and reuses it on a second fetch.
	plain := withDefaults(RunOptions{Ctx: t.Context(), SourceRoot: "one/src",
		SourceWorkspace: workspace, Launch: true, Stdout: io.Discard, Stderr: io.Discard})
	if err := (Local{}).fetch(fetch, plain); err != nil {
		t.Fatal(err)
	}
	plainReceiptPath, err := sourceReceiptPath(plain.SourceRoot, fetch.Dest)
	if err != nil {
		t.Fatal(err)
	}
	beforeBytes, err := os.ReadFile(plainReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(plainReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := (Local{}).fetch(fetch, plain); err != nil {
		t.Fatal(err)
	}
	afterBytes, err := os.ReadFile(plainReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(plainReceiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(beforeBytes, afterBytes) || !os.SameFile(beforeInfo, afterInfo) || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("unchanged held fetch rewrote its receipt")
	}
	checkout := filepath.Join("one/src", fetch.Dest)
	gitmodules, err := os.ReadFile(filepath.Join(checkout, ".gitmodules"))
	if err != nil || string(gitmodules) != wliFixtureGitmodules {
		t.Fatalf("held fetch lost the upstream .gitmodules bytes: %q, %v", gitmodules, err)
	}
	for _, directory := range wliFixtureGitlinkDirectories {
		entries, err := os.ReadDir(filepath.Join(checkout, filepath.FromSlash(directory)))
		if err != nil {
			t.Fatalf("uninitialized gitlink directory %s missing after held fetch: %v", directory, err)
		}
		if len(entries) != 0 {
			t.Fatalf("gitlink directory %s materialized initialized contents: %v", directory, entries)
		}
	}
	receiptPath, err := sourceReceiptPath(plain.SourceRoot, fetch.Dest)
	if err != nil {
		t.Fatal(err)
	}
	receipt, exists, err := readSourceReceiptContext(t.Context(), receiptPath)
	if err != nil || !exists || receipt.Commit != fetch.Commit || receipt.TreeHash != fetch.TreeHash {
		t.Fatalf("held fetch receipt mismatch: %+v exists=%t error=%v", receipt, exists, err)
	}

	// Phase 2: the vendor route packs the held checkout into its input carrier,
	// transfers it, copies the returned closure back, and only then issues the
	// receipt the build materializer will authenticate.
	vendored := fetch
	vendored.VendorCmd = vendorCommand
	vendored.VendorImage = "sha256:" + strings.Repeat("b", 64)
	vendored.VendorSpecDigest, err = ir.VendorSpecIdentity(vendored.VendorCmd, vendored.VendorImage)
	if err != nil {
		t.Fatal(err)
	}
	if err := vendored.Validate(); err != nil {
		t.Fatal(err)
	}
	vendorRuntime := sourceVendorRuntimeFixture(t, "linux/"+runtime.GOARCH)
	vendorRuntime.result = wliVendorCarrier(t)
	opts := withDefaults(RunOptions{Ctx: t.Context(), SourceRoot: "two/src",
		SourceWorkspace: workspace, Launch: true, Stdout: io.Discard, Stderr: io.Discard})
	held := &heldRuntimeExecutable{}
	calls := 0
	err = (Local{}).fetchWithSourceVendorAdmission(vendored, opts,
		func(_ context.Context, selected ir.Node, _ string) (*sourceVendor, error) {
			calls++
			if !slices.Equal(selected.VendorCmd, vendorCommand) {
				t.Fatal("vendor handoff lost the declared vendor command")
			}
			return &sourceVendor{held: held, runtime: vendorRuntime, image: vendored.VendorImage,
				platform: "linux/" + runtime.GOARCH, network: vendored.Network, command: vendorCommand}, nil
		})
	if err != nil || calls != 1 {
		t.Fatalf("held vendor transport failed: calls=%d error=%v", calls, err)
	}
	if len(vendorRuntime.input) == 0 {
		t.Fatal("vendor handoff transferred no source carrier")
	}
	handoff := filepath.Join(t.TempDir(), "carrier.tar")
	if err := os.WriteFile(handoff, vendorRuntime.input, 0o600); err != nil {
		t.Fatal(err)
	}
	archive, err := os.Open(handoff)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	staged := filepath.Join(t.TempDir(), "handoff")
	if err := transportarchive.UnpackOpen(t.Context(), archive, staged, []string{"source"}); err != nil {
		t.Fatalf("vendor input carrier rejected by the transport owner: %v", err)
	}
	handed, err := os.ReadFile(filepath.Join(staged, "source", ".gitmodules"))
	if err != nil || string(handed) != wliFixtureGitmodules {
		t.Fatalf("vendor input carrier lost .gitmodules: %q, %v", handed, err)
	}
	for _, directory := range wliFixtureGitlinkDirectories {
		entries, err := os.ReadDir(filepath.Join(staged, "source", filepath.FromSlash(directory)))
		if err != nil || len(entries) != 0 {
			t.Fatalf("vendor input carrier lost uninitialized gitlink directory %s: %v / %v", directory, entries, err)
		}
	}
	vendoredCheckout := filepath.Join("two/src", fetch.Dest)
	if _, err := os.Stat(filepath.Join(vendoredCheckout, "vendor", "modules.txt")); err != nil {
		t.Fatalf("vendored copy-back lost the vendor manifest: %v", err)
	}
	if preserved, err := os.ReadFile(filepath.Join(vendoredCheckout, ".gitmodules")); err != nil || string(preserved) != wliFixtureGitmodules {
		t.Fatalf("vendored closure lost .gitmodules: %q, %v", preserved, err)
	}
	for _, directory := range wliFixtureGitlinkDirectories {
		entries, err := os.ReadDir(filepath.Join(vendoredCheckout, filepath.FromSlash(directory)))
		if err != nil || len(entries) != 0 {
			t.Fatalf("vendored closure initialized gitlink %s: %v / %v", directory, entries, err)
		}
	}
	receiptPath, err = sourceReceiptPath(opts.SourceRoot, fetch.Dest)
	if err != nil {
		t.Fatal(err)
	}
	receipt, exists, err = readSourceReceiptContext(t.Context(), receiptPath)
	if err != nil || !exists || receipt.Commit != fetch.Commit || receipt.TreeHash != fetch.TreeHash ||
		receipt.VendorSpecDigest != vendored.VendorSpecDigest {
		t.Fatalf("vendored receipt mismatch: %+v exists=%t error=%v", receipt, exists, err)
	}
	closure, err := sourceTreeDigestContext(t.Context(), vendoredCheckout)
	if err != nil || receipt.ClosureDigest != closure {
		t.Fatalf("receipt closure binding lost: %s vs %s, %v", receipt.ClosureDigest, closure, err)
	}

	// Phase 3: the build materializer authenticates the receipt, re-hashes the
	// closure, and copies the exact bytes into its private snapshot with the
	// declared write mountpoint.
	build := wliAgentBuildNode(t, vendored, buildCommand)
	snapshot, err := materializeSourceUncached(build, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.cleanup()
	if snapshot.InputDigest == "" {
		t.Fatal("materialization produced no resolved-input identity")
	}
	materialized := snapshot.Dir
	for name, want := range map[string]string{
		".gitmodules":        wliFixtureGitmodules,
		"go.mod":             "module dsx-identity\n\ngo 1.26.0\n",
		"vendor/modules.txt": "# dsx-identity v1.15.0 fixture vendor manifest\n",
	} {
		got, err := os.ReadFile(filepath.Join(materialized, filepath.FromSlash(name)))
		if err != nil || string(got) != want {
			t.Fatalf("materialized snapshot lost or changed %s: %q, %v", name, got, err)
		}
	}
	for _, directory := range wliFixtureGitlinkDirectories {
		entries, err := os.ReadDir(filepath.Join(materialized, filepath.FromSlash(directory)))
		if err != nil || len(entries) != 0 {
			t.Fatalf("materialized snapshot initialized gitlink %s: %v / %v", directory, entries, err)
		}
	}
	if info, err := os.Stat(filepath.Join(materialized, "nvkr-output")); err != nil || !info.IsDir() {
		t.Fatalf("materialized snapshot missing the declared write mountpoint: %v", err)
	}
}

// A tampered closure or receipt inconsistent with that closure must fail
// materialization. Held fetch issues each initial receipt; one case edits that
// receipt afterward. Untampered controls use the same materialization options.
func TestWLIIMDSAgentHeldSourceTransportRejectsTampering(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("actual held source transport requires Linux")
	}
	vendorCommand, buildCommand := wliCatalogCommands(t)
	for _, tc := range []struct {
		name    string
		vendor  bool
		message string
		tamper  func(t *testing.T, checkout, receiptPath string)
	}{
		{name: "untampered plain"},
		{name: "untampered vendored", vendor: true},
		{
			name:    "initialized gitlink member",
			message: "source changed after fetch",
			tamper: func(t *testing.T, checkout, _ string) {
				if err := os.WriteFile(filepath.Join(checkout, "third-party", "doca-platform-foundation", "intruder"),
					[]byte("uninitialized no longer"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:    "changed vendored member",
			vendor:  true,
			message: "source changed after fetch",
			tamper: func(t *testing.T, checkout, _ string) {
				if err := os.WriteFile(filepath.Join(checkout, "vendor", "modules.txt"), []byte("drifted manifest\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:    "receipt inconsistent with closure",
			message: "source changed after fetch",
			tamper: func(t *testing.T, _, receiptPath string) {
				raw, err := os.ReadFile(receiptPath)
				if err != nil {
					t.Fatal(err)
				}
				var receipt sourceReceipt
				if err := canonical.UnmarshalExact(raw, &receipt); err != nil {
					t.Fatal(err)
				}
				receipt.ClosureDigest = "sha256:" + strings.Repeat("f", 64)
				forged, err := canonical.Marshal(receipt)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(receiptPath, forged, 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			workspace, err := HoldSourceWorkspace(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer workspace.Close()
			fetch := wliSourceFixture(t, "nvidia-workload-identity")
			if tc.vendor {
				fetch.VendorCmd = slices.Clone(vendorCommand)
				fetch.VendorImage = "sha256:" + strings.Repeat("b", 64)
				fetch.VendorSpecDigest, err = ir.VendorSpecIdentity(fetch.VendorCmd, fetch.VendorImage)
				if err != nil {
					t.Fatal(err)
				}
				vendorRuntime := sourceVendorRuntimeFixture(t, "linux/"+runtime.GOARCH)
				vendorRuntime.result = wliVendorCarrier(t)
				declared := fetch
				err = (Local{}).fetchWithSourceVendorAdmission(fetch, withDefaults(RunOptions{Ctx: t.Context(),
					SourceRoot: "src", SourceWorkspace: workspace, Launch: true, Stdout: io.Discard, Stderr: io.Discard}),
					func(_ context.Context, _ ir.Node, _ string) (*sourceVendor, error) {
						return &sourceVendor{held: &heldRuntimeExecutable{}, runtime: vendorRuntime,
							image: declared.VendorImage, platform: "linux/" + runtime.GOARCH,
							network: declared.Network, command: declared.VendorCmd}, nil
					})
				if err != nil {
					t.Fatalf("fixture vendored fetch: %v", err)
				}
			} else {
				opts := withDefaults(RunOptions{Ctx: t.Context(), SourceRoot: "src",
					SourceWorkspace: workspace, Launch: true, Stdout: io.Discard, Stderr: io.Discard})
				if err := (Local{}).fetch(fetch, opts); err != nil {
					t.Fatal(err)
				}
			}
			receiptPath, err := sourceReceiptPath("src", fetch.Dest)
			if err != nil {
				t.Fatal(err)
			}
			if _, exists, err := readSourceReceiptContext(t.Context(), receiptPath); err != nil || !exists {
				t.Fatalf("fixture receipt: exists=%t error=%v", exists, err)
			}
			if tc.tamper != nil {
				tc.tamper(t, filepath.Join("src", fetch.Dest), receiptPath)
			}
			build := wliAgentBuildNode(t, fetch, buildCommand)
			opts := withDefaults(RunOptions{Ctx: t.Context(), SourceRoot: "src", SourceWorkspace: workspace, Stdout: io.Discard, Stderr: io.Discard})
			snapshot, err := materializeSourceUncached(build, opts)
			if err == nil {
				snapshot.cleanup()
			}
			if tc.tamper == nil {
				if err != nil || snapshot == nil {
					t.Fatalf("untampered control failed: %v", err)
				}
				return
			}
			if code(err) != e.CodeBuildFailed || !strings.Contains(err.Error(), tc.message) || snapshot != nil {
				t.Fatalf("tampered %s refused at wrong boundary: %v", tc.name, err)
			}
			if _, err := os.Stat("snapshots"); !os.IsNotExist(err) {
				t.Fatalf("refusal created snapshot root: %v", err)
			}
		})
	}
}
