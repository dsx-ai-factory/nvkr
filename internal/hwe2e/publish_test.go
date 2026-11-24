// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

// Contracts for publishing an NVKR-built node image for the hardware E2E workflow. The pull and the object store
// are fake executables (see faketools_test.go), so these say what the publisher requires and refuses and nothing
// about S3, the HTTPS front end, a registry or hardware. A run against them is evidence of that run only.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	nerrors "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const (
	publishOutput  = "node-image-amd64"
	publishVersion = "v0.24.0-beta.12"
	publishBucket  = "example-assets"
	publishRoot    = "pkgs"
	publishPrefix  = "nvkr/os-image"
	publishDomain  = "cdn.example.test"
	publishSlot    = "os-image-amd64"
	publishRun     = "https://github.com/example/nvkr/actions/runs/123456/attempts/1"
)

var (
	publishRegistryRef = "ghcr.io/example/nvkr-release@sha256:" + strings.Repeat("a", 64)
	publishReference   = "oci://" + publishRegistryRef
	publishImage       = newQCOW2().image(payloadOf("synthetic payload ", 64))
	publishDigest      = sumHex(publishImage)
	publishKey         = publishKeyFor("amd64", publishDigest)
)

func publishKeyFor(arch, digest string) string {
	return strings.Join([]string{publishRoot, publishPrefix, publishVersion, arch, digest, publishOutput + ".qcow2"}, "/")
}

// publishExitCodes names the code each exit status stands for, so a test pins both.
var publishExitCodes = map[int]nerrors.Code{
	2:  nerrors.CodeUsage,
	13: nerrors.CodeUnsupported,
	17: nerrors.CodePreflightFailed,
	20: nerrors.CodeFetchFailed,
	22: nerrors.CodeVerifyFailed,
	30: nerrors.CodeUnavailable,
	31: nerrors.CodeTimeout,
	47: nerrors.CodeImmutableViolation,
}

// pullResult is what `nvkr release pull --format json` prints for a disk output.
func pullResult(image []byte, arch string) map[string]any {
	return map[string]any{
		"schemaVersion": "nvkr.dev/release-pull-result/v1",
		"reference":     publishRegistryRef,
		"output": map[string]any{
			"name": publishOutput, "kind": "disk-image", "digest": "sha256:" + strings.Repeat("b", 64),
			"buildDigest": "sha256:" + strings.Repeat("c", 64), "os": "linux", "arch": arch,
		},
		"destination": "unused",
		"payload":     "files",
		"files":       []any{map[string]any{"path": publishOutput + ".qcow2", "sha256": sumHex(image), "size": len(image)}},
	}
}

func outputOf(result map[string]any) map[string]any { return result["output"].(map[string]any) }
func fileOf(result map[string]any) map[string]any {
	return result["files"].([]any)[0].(map[string]any)
}

// harnessSetup is what one test changes about its fake tools.
type harnessSetup struct {
	image []byte
	arch  string
	edit  func(map[string]any)
	raw   *string
	pull  fakeNvkrConfig
	aws   fakeAWSConfig
}

type harnessOption func(*harnessSetup)

func withImage(image []byte) harnessOption { return func(s *harnessSetup) { s.image = image } }
func withArch(arch string) harnessOption   { return func(s *harnessSetup) { s.arch = arch } }
func withPullEdit(edit func(map[string]any)) harnessOption {
	return func(s *harnessSetup) { s.edit = edit }
}
func withPullOutput(text string) harnessOption { return func(s *harnessSetup) { s.raw = &text } }
func withPullFailure(code int, stderr string) harnessOption {
	return func(s *harnessSetup) { s.pull.Exit, s.pull.Stderr = code, stderr }
}
func withSymlinkedImage() harnessOption { return func(s *harnessSetup) { s.pull.Symlink = true } }
func withAWS(edit func(*fakeAWSConfig)) harnessOption {
	return func(s *harnessSetup) { edit(&s.aws) }
}

// publishHarness is one test's fake tools, bucket, scratch space and call logs.
type publishHarness struct {
	t       *testing.T
	root    string
	store   string
	nvkr    string
	aws     string
	nvkrLog string
	awsLog  string
}

func newPublishHarness(t *testing.T, options ...harnessOption) *publishHarness {
	t.Helper()
	setup := harnessSetup{image: publishImage, arch: "amd64"}
	for _, option := range options {
		option(&setup)
	}
	root := t.TempDir()
	h := &publishHarness{
		t: t, root: root, store: filepath.Join(root, "bucket"),
		nvkrLog: filepath.Join(root, "nvkr.log"), awsLog: filepath.Join(root, "aws.log"),
	}
	if err := os.Mkdir(h.store, 0o700); err != nil {
		t.Fatal(err)
	}
	result := pullResult(setup.image, setup.arch)
	if setup.edit != nil {
		setup.edit(result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	setup.pull.Result = string(encoded)
	if setup.raw != nil {
		setup.pull.Result = *setup.raw
	}
	setup.pull.Log, setup.pull.FileName, setup.pull.Image = h.nvkrLog, publishOutput+".qcow2", setup.image
	setup.aws.Log, setup.aws.Store = h.awsLog, h.store
	h.nvkr = h.tool("nvkr", setup.pull)
	h.aws = h.tool("aws", setup.aws)
	return h
}

// tool writes the fake's configuration and a link, named for the tool, to this test binary.
func (h *publishHarness) tool(name string, config any) string {
	h.t.Helper()
	data, err := json.Marshal(config)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.root, name+".json"), data, 0o600); err != nil {
		h.t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		h.t.Fatal(err)
	}
	link := filepath.Join(h.root, name)
	if err := os.Symlink(self, link); err != nil {
		h.t.Fatal(err)
	}
	return link
}

func (h *publishHarness) seed(key string, data []byte) {
	h.t.Helper()
	target := filepath.Join(append([]string{h.store, publishBucket}, strings.Split(key, "/")...)...)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0o600); err != nil {
		h.t.Fatal(err)
	}
}

// stored returns what the bucket holds at key, or nil.
func (h *publishHarness) stored(key string) []byte {
	h.t.Helper()
	target := filepath.Join(append([]string{h.store, publishBucket}, strings.Split(key, "/")...)...)
	data, err := os.ReadFile(target)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		h.t.Fatal(err)
	}
	return data
}

func readCalls(t *testing.T, path string) [][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var call []string
		if err := json.Unmarshal([]byte(line), &call); err != nil {
			t.Fatal(err)
		}
		calls = append(calls, call)
	}
	return calls
}

func (h *publishHarness) pullCalls() [][]string { return readCalls(h.t, h.nvkrLog) }
func (h *publishHarness) awsCalls() [][]string  { return readCalls(h.t, h.awsLog) }

// awsOps names each object store call head, put-file, put-text or get.
func (h *publishHarness) awsOps() []string {
	var ops []string
	for _, call := range h.awsCalls() {
		switch {
		case call[0] == "s3api" && call[1] == "head-object":
			ops = append(ops, "head")
		case call[0] == "s3" && call[1] == "cp" && call[2] == "-":
			ops = append(ops, "put-text")
		case call[0] == "s3" && call[1] == "cp" && call[3] == "-":
			ops = append(ops, "get")
		case call[0] == "s3" && call[1] == "cp":
			ops = append(ops, "put-file")
		default:
			ops = append(ops, call[0]+" "+call[1])
		}
	}
	return ops
}

func (h *publishHarness) uploaded() []string {
	var puts []string
	for _, op := range h.awsOps() {
		if strings.HasPrefix(op, "put") {
			puts = append(puts, op)
		}
	}
	return puts
}

func (h *publishHarness) recordDir() string { return filepath.Join(h.root, "record") }

func (h *publishHarness) recordExists() bool { return pathExists(h.recordDir()) }

func (h *publishHarness) record(slot string) map[string]any {
	h.t.Helper()
	data, err := os.ReadFile(filepath.Join(h.recordDir(), slot+".json"))
	if err != nil {
		h.t.Fatalf("no publication record: %v", err)
	}
	decoded, err := decodeJSON(data)
	if err != nil {
		h.t.Fatal(err)
	}
	return decoded.(map[string]any)
}

// untouched asserts that nothing was written to the object store and no record exists.
func (h *publishHarness) untouched() {
	h.t.Helper()
	if puts := h.uploaded(); len(puts) != 0 {
		h.t.Errorf("object store writes %v, want none", puts)
	}
	if h.recordExists() {
		h.t.Errorf("a record exists at %s, want none", h.recordDir())
	}
}

// omit leaves a flag off the command line.
const omit = "\x00omit"

type overrides map[string]string

var publishFlagOrder = []string{
	"--slot", "--reference", "--output", "--version", "--s3-uri", "--prefix", "--cdn-domain", "--region",
	"--work-dir", "--out", "--run-url", "--nvkr", "--aws",
}

func (h *publishHarness) argv(over overrides) []string {
	values := map[string]string{
		"--slot": publishSlot, "--reference": publishReference, "--output": publishOutput, "--version": publishVersion,
		"--s3-uri": "s3://" + publishBucket + "/" + publishRoot, "--prefix": publishPrefix, "--cdn-domain": publishDomain,
		"--region": "us-west-2", "--work-dir": filepath.Join(h.root, "pull"), "--out": h.recordDir(),
		"--run-url": publishRun, "--nvkr": h.nvkr, "--aws": h.aws,
	}
	var args []string
	for _, name := range publishFlagOrder {
		value := values[name]
		if replacement, ok := over[name]; ok {
			value = replacement
		}
		if value != omit {
			args = append(args, name, value)
		}
	}
	if _, ok := over["--dry-run"]; ok {
		args = append(args, "--dry-run")
	}
	return args
}

type publishResult struct {
	code   int
	stdout string
	stderr string
}

func (h *publishHarness) run(over overrides) publishResult {
	h.t.Helper()
	return h.runWith(context.Background(), defaultPublishLimits, over)
}

func (h *publishHarness) runWith(ctx context.Context, limits publishLimits, over overrides) publishResult {
	h.t.Helper()
	return h.runArgs(ctx, limits, h.argv(over))
}

func (h *publishHarness) runArgs(ctx context.Context, limits publishLimits, args []string) publishResult {
	h.t.Helper()
	var stdout, stderr bytes.Buffer
	err := publishNodeImage(ctx, args, &stdout, &stderr, limits)
	return publishResult{code: report(&stderr, err), stdout: stdout.String(), stderr: stderr.String()}
}

// succeeds fails the test unless the run ended in exit 0.
func (r publishResult) succeeds(t *testing.T) publishResult {
	t.Helper()
	if r.code != 0 {
		t.Fatalf("exit = %d, want 0; stderr: %s", r.code, r.stderr)
	}
	return r
}

// fails fails the test unless the run ended with exactly this exit status, its code and a message naming the cause.
func (r publishResult) fails(t *testing.T, exit int, fragment string) {
	t.Helper()
	if r.code != exit {
		t.Fatalf("exit = %d, want %d; stderr: %s", r.code, exit, r.stderr)
	}
	if want := "[" + string(publishExitCodes[exit]) + "]"; !strings.Contains(r.stderr, want) {
		t.Errorf("stderr = %q, want code %s", r.stderr, want)
	}
	if !strings.Contains(r.stderr, fragment) {
		t.Errorf("stderr = %q, want %q", r.stderr, fragment)
	}
	if !strings.HasPrefix(r.stderr, "nvkr: [NVKR-E-") {
		t.Errorf("stderr = %q, want one coded line starting nvkr: [NVKR-E-", r.stderr)
	}
	if strings.Count(strings.TrimRight(r.stderr, "\n"), "\n") != 0 {
		t.Errorf("stderr = %q, want a single line", r.stderr)
	}
	if r.stdout != "" {
		t.Errorf("stdout = %q on a failure, want nothing", r.stdout)
	}
}

func TestPublishRefusesRequestsItCannotPublishBeforeRunningAnything(t *testing.T) {
	cases := []struct {
		name     string
		over     overrides
		exit     int
		fragment string
	}{
		{"a tag in place of a digest", overrides{"--reference": "oci://ghcr.io/example/nvkr-release:latest"}, 13, "pinned by digest"},
		{"a reference with no digest at all", overrides{"--reference": "oci://ghcr.io/example/nvkr-release"}, 13, "pinned by digest"},
		{"a reference with a short digest", overrides{"--reference": "oci://ghcr.io/example/nvkr-release@sha256:" + strings.Repeat("a", 63)}, 13, "pinned by digest"},
		{"a reference without oci://", overrides{"--reference": publishRegistryRef}, 13, "oci://"},
		{"an output name that climbs", overrides{"--output": "../node-image"}, 13, "plain output name"},
		{"an output name with a slash", overrides{"--output": "nested/node-image"}, 13, "plain output name"},
		{"a version without a v", overrides{"--version": "0.24.0"}, 13, "version"},
		{"a prefix that is not under nvkr/", overrides{"--prefix": "os-image"}, 13, "nvkr/"},
		{"a prefix that borrows the name", overrides{"--prefix": "nvkrx/os-image"}, 13, "nvkr/"},
		{"a prefix that climbs out", overrides{"--prefix": "nvkr/../os-image"}, 13, "nvkr/"},
		{"a prefix with an empty segment", overrides{"--prefix": "nvkr//os-image"}, 13, "nvkr/"},
		{"an absolute prefix", overrides{"--prefix": "/nvkr/os-image"}, 13, "nvkr/"},
		{"a root that is not s3", overrides{"--s3-uri": "https://example-assets/pkgs"}, 17, "s3://"},
		{"a root with a trailing slash", overrides{"--s3-uri": "s3://" + publishBucket + "/" + publishRoot + "/"}, 17, "trailing slash"},
		{"a domain with a scheme", overrides{"--cdn-domain": "https://cdn.example.test"}, 17, "host name"},
		{"an upper-case domain", overrides{"--cdn-domain": "CDN.example.test"}, 17, "host name"},
		{"a region that is not one", overrides{"--region": "west"}, 17, "region"},
		{"no run to name", overrides{"--run-url": omit}, 13, "run-url"},
		{"a run URL that is not a run", overrides{"--run-url": "https://github.com/example/nvkr/pull/1"}, 13, "run-url"},
		{"a run URL with text before it", overrides{"--run-url": "see " + publishRun}, 13, "run-url"},
		{"a run URL with text after it", overrides{"--run-url": publishRun + " and more"}, 13, "run-url"},
		{"a run URL with a query", overrides{"--run-url": publishRun + "?check_suite_focus=true"}, 13, "run-url"},
		{"a run URL over http", overrides{"--run-url": strings.Replace(publishRun, "https", "http", 1)}, 13, "run-url"},
		{"a run URL on another host", overrides{"--run-url": strings.Replace(publishRun, "github.com", "ci.example.test", 1)}, 13, "run-url"},
		{"a run URL of run zero", overrides{"--run-url": "https://github.com/example/nvkr/actions/runs/0"}, 13, "run-url"},
		{"a run URL of attempt zero", overrides{"--run-url": "https://github.com/example/nvkr/actions/runs/5/attempts/0"}, 13, "run-url"},
		{"a dry run still checks a run URL it was given", overrides{"--dry-run": "true", "--run-url": "https://github.com/example/nvkr/pull/1"}, 13, "run-url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newPublishHarness(t)
			h.run(tc.over).fails(t, tc.exit, tc.fragment)
			if calls := h.pullCalls(); len(calls) != 0 {
				t.Errorf("the pull ran: %v", calls)
			}
			if calls := h.awsCalls(); len(calls) != 0 {
				t.Errorf("the object store was called: %v", calls)
			}
			if pathExists(filepath.Join(h.root, "pull")) {
				t.Error("the work directory was created")
			}
		})
	}
}

func TestPublishCommandLine(t *testing.T) {
	t.Run("an unknown slot is a usage error", func(t *testing.T) {
		newPublishHarness(t).run(overrides{"--slot": "os-image-riscv"}).fails(t, 2, "invalid choice")
	})

	for _, required := range []string{"--slot", "--reference", "--output", "--version", "--s3-uri", "--prefix", "--cdn-domain", "--region", "--work-dir", "--out"} {
		t.Run("without "+required, func(t *testing.T) {
			newPublishHarness(t).run(overrides{required: omit}).fails(t, 2, "the following arguments are required: "+required)
		})
	}

	t.Run("an unknown flag and a positional argument are usage errors", func(t *testing.T) {
		h := newPublishHarness(t)
		h.runArgs(context.Background(), defaultPublishLimits, append(h.argv(nil), "--bogus")).fails(t, 2, "bogus")
		h.runArgs(context.Background(), defaultPublishLimits, append(h.argv(nil), "extra")).fails(t, 2, `unexpected argument "extra"`)
	})

	t.Run("a missing tool is a failed precondition", func(t *testing.T) {
		for _, flagName := range []string{"--nvkr", "--aws"} {
			h := newPublishHarness(t)
			h.run(overrides{flagName: "/nonexistent/tool"}).fails(t, 17, "is not installed")
			h.untouched()
		}
	})

	t.Run("a dry run needs no object store tool", func(t *testing.T) {
		h := newPublishHarness(t)
		h.run(overrides{"--dry-run": "true", "--aws": "/nonexistent/aws", "--run-url": omit}).succeeds(t)
	})

	t.Run("an existing record directory is refused before the pull", func(t *testing.T) {
		h := newPublishHarness(t)
		if err := os.Mkdir(h.recordDir(), 0o700); err != nil {
			t.Fatal(err)
		}
		h.run(nil).fails(t, 47, "refusing to replace")
		if calls := h.pullCalls(); len(calls) != 0 {
			t.Errorf("the pull ran: %v", calls)
		}
	})

	t.Run("an existing work directory is refused before the pull", func(t *testing.T) {
		h := newPublishHarness(t)
		if err := os.Mkdir(filepath.Join(h.root, "pull"), 0o700); err != nil {
			t.Fatal(err)
		}
		h.run(nil).fails(t, 17, "must not exist")
		if calls := h.pullCalls(); len(calls) != 0 {
			t.Errorf("the pull ran: %v", calls)
		}
	})

	t.Run("a work directory without a parent is refused before the pull", func(t *testing.T) {
		h := newPublishHarness(t)
		h.run(overrides{"--work-dir": filepath.Join(h.root, "missing", "pull")}).fails(t, 17, "its parent must")
		if calls := h.pullCalls(); len(calls) != 0 {
			t.Errorf("the pull ran: %v", calls)
		}
	})

	t.Run("the pull is asked for exactly the digest-pinned output", func(t *testing.T) {
		h := newPublishHarness(t)
		h.run(nil).succeeds(t)
		calls := h.pullCalls()
		if len(calls) != 1 {
			t.Fatalf("%d pulls, want 1: %v", len(calls), calls)
		}
		want := []string{
			"release", "pull", "--reference", publishRegistryRef, "--output", publishOutput,
			"--out", filepath.Join(h.root, "pull"), "--format", "json", "--timeout", "35m",
		}
		if !reflect.DeepEqual(calls[0], want) {
			t.Errorf("pull = %v\nwant %v", calls[0], want)
		}
	})
}

func TestPublishPullFailures(t *testing.T) {
	t.Run("a failed pull publishes nothing", func(t *testing.T) {
		h := newPublishHarness(t, withPullFailure(21, "boom"))
		h.run(nil).fails(t, 20, "release pull exited 21: boom")
		if calls := h.awsCalls(); len(calls) != 0 {
			t.Errorf("the object store was called: %v", calls)
		}
		h.untouched()
	})

	t.Run("a result it does not recognize is refused", func(t *testing.T) {
		cases := []struct {
			name     string
			edit     func(map[string]any)
			exit     int
			fragment string
		}{
			{"another schema", func(r map[string]any) { r["schemaVersion"] = "nvkr.dev/release-pull-result/v2" }, 22, `has schema "nvkr.dev/release-pull-result/v2", not`},
			{"no schema", func(r map[string]any) { delete(r, "schemaVersion") }, 22, "has schema null, not"},
			{"another reference", func(r map[string]any) {
				r["reference"] = "ghcr.io/example/other@sha256:" + strings.Repeat("d", 64)
			}, 22, "different reference"},
			{"another output", func(r map[string]any) { outputOf(r)["name"] = "other" }, 22, "different reference or output"},
			{"not a disk", func(r map[string]any) { outputOf(r)["kind"] = "executable" }, 13, `is a "executable", not a disk-image`},
			{"no kind", func(r map[string]any) { delete(outputOf(r), "kind") }, 13, "is a null, not a disk-image"},
			{"the wrong architecture", func(r map[string]any) { outputOf(r)["arch"] = "arm64" }, 22, `built for "arm64", but the slot os-image-amd64 is amd64`},
			{"no architecture", func(r map[string]any) { delete(outputOf(r), "arch") }, 22, "declares no architecture"},
			{"a null architecture", func(r map[string]any) { outputOf(r)["arch"] = nil }, 22, "declares no architecture"},
			{"an architecture that is not text", func(r map[string]any) { outputOf(r)["arch"] = []any{"amd64"} }, 22, `built for ["amd64"]`},
			{"two files", func(r map[string]any) {
				r["files"] = append(r["files"].([]any), map[string]any{"path": "extra.qcow2", "sha256": publishDigest, "size": 1})
			}, 22, "expected only"},
			{"another file name", func(r map[string]any) { fileOf(r)["path"] = "other.qcow2" }, 22, "expected only"},
			{"files that are not a list", func(r map[string]any) { r["files"] = "nope" }, 22, "expected only"},
			{"a file that is not an object", func(r map[string]any) { r["files"] = []any{"nope"} }, 22, "expected only"},
			{"no digest", func(r map[string]any) { delete(fileOf(r), "sha256") }, 22, "no usable sha256"},
			{"a digest that is not text", func(r map[string]any) { fileOf(r)["sha256"] = 5 }, 22, "no usable sha256"},
			{"a boolean size", func(r map[string]any) { fileOf(r)["size"] = true }, 22, "no usable sha256"},
			{"a size of zero", func(r map[string]any) { fileOf(r)["size"] = 0 }, 22, "no usable sha256"},
			{"a negative size", func(r map[string]any) { fileOf(r)["size"] = -1 }, 22, "no usable sha256"},
			{"a size that is text", func(r map[string]any) { fileOf(r)["size"] = strconv.Itoa(len(publishImage)) }, 22, "no usable sha256"},
			{"a fractional size", func(r map[string]any) { fileOf(r)["size"] = 1.5 }, 22, "no usable sha256"},
			{"an upper-case digest", func(r map[string]any) { fileOf(r)["sha256"] = strings.ToUpper(publishDigest) }, 22, "no usable sha256"},
			{"a short digest", func(r map[string]any) { fileOf(r)["sha256"] = publishDigest[:63] }, 22, "no usable sha256"},
			{"no files", func(r map[string]any) { delete(r, "files") }, 22, "understands"},
			{"no output", func(r map[string]any) { delete(r, "output") }, 22, "understands"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				h := newPublishHarness(t, withPullEdit(tc.edit))
				h.run(nil).fails(t, tc.exit, tc.fragment)
				h.untouched()
			})
		}
	})

	t.Run("output that is not one JSON object is refused", func(t *testing.T) {
		for name, text := range map[string]string{
			"nothing": "", "not JSON": "pulled, thanks", "a list": "[]", "a string": `"done"`, "two values": "{} {}",
		} {
			t.Run(name, func(t *testing.T) {
				h := newPublishHarness(t, withPullOutput(text))
				h.run(nil).fails(t, 22, "no result this script understands")
				h.untouched()
			})
		}
	})

	t.Run("an image that declares no virtual size publishes nothing", func(t *testing.T) {
		image := newQCOW2().with(func(s *qcow2Spec) { s.size = 0 }).image(payloadOf("synthetic payload ", 64))
		h := newPublishHarness(t, withImage(image))
		h.run(nil).fails(t, 13, "declares no virtual size")
		h.untouched()
	})

	t.Run("the bytes on disk must be the bytes the pull reported", func(t *testing.T) {
		cases := []struct {
			name     string
			edit     func(map[string]any)
			fragment string
		}{
			{"digest", func(r map[string]any) { fileOf(r)["sha256"] = strings.Repeat("e", 64) }, "hashes to"},
			{"size", func(r map[string]any) { fileOf(r)["size"] = len(publishImage) + 1 }, "bytes; release pull reported"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				h := newPublishHarness(t, withPullEdit(tc.edit))
				h.run(nil).fails(t, 22, tc.fragment)
				h.untouched()
			})
		}
	})

	t.Run("a symlink in place of the image is refused", func(t *testing.T) {
		h := newPublishHarness(t, withSymlinkedImage())
		h.run(nil).fails(t, 22, "not a regular file")
		h.untouched()
	})

	t.Run("only a plain qcow2 is published", func(t *testing.T) {
		cases := []struct {
			name     string
			image    []byte
			fragment string
		}{
			{"not a qcow2", bytes.Repeat([]byte("not a disk image at all, only text "), 8), "qcow2 magic"},
			{"a backing file", newQCOW2().with(func(s *qcow2Spec) { s.backing = 4096 }).image(bytes.Repeat([]byte("x"), 64)), "backing file"},
			{"an encrypted image", newQCOW2().with(func(s *qcow2Spec) { s.crypt = 2 }).image(bytes.Repeat([]byte("x"), 64)), "encrypted"},
			{"an unknown version", newQCOW2().with(func(s *qcow2Spec) { s.version = 4 }).image(bytes.Repeat([]byte("x"), 64)), "version 4"},
			{"an external data file", newQCOW2().with(func(s *qcow2Spec) { s.incompatible = qcow2IncompatibleExternalDataFile }).image(bytes.Repeat([]byte("x"), 64)), "external data file"},
			{"too small", []byte{'Q', 'F', 'I', 0xfb}, "too small"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				h := newPublishHarness(t, withImage(tc.image))
				h.run(nil).fails(t, 13, tc.fragment)
				h.untouched()
			})
		}
	})
}

func canonicalFile(t *testing.T, record map[string]any) []byte {
	t.Helper()
	data, err := marshalRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestPublishUploadsThenReadsBackBeforeItRecordsAnything(t *testing.T) {
	h := newPublishHarness(t)
	got := h.run(nil).succeeds(t)
	if !bytes.Equal(h.stored(publishKey), publishImage) {
		t.Error("the image is not in the object store")
	}
	if want := publishDigest; string(h.stored(publishKey+".sha256")) != want {
		t.Errorf("sidecar = %q, want %q", h.stored(publishKey+".sha256"), want)
	}
	if ops, want := h.awsOps(), []string{"head", "put-file", "head", "get", "head", "put-text", "get"}; !reflect.DeepEqual(ops, want) {
		t.Errorf("object store calls = %v, want %v", ops, want)
	}
	containsAll(t, got.stdout, "sha256 "+publishDigest, "uploaded", "s3://"+publishBucket+"/"+publishKey)
	if got.stderr != "" {
		t.Errorf("stderr = %q, want nothing", got.stderr)
	}
}

func TestPublishCallsTheObjectStoreExactly(t *testing.T) {
	h := newPublishHarness(t)
	h.run(nil).succeeds(t)
	object := "s3://" + publishBucket + "/" + publishKey
	region := []string{"--region", "us-west-2"}
	withRegion := func(args ...string) []string { return append(args, region...) }
	head := func(key string) []string {
		return withRegion("s3api", "head-object", "--bucket", publishBucket, "--key", key, "--output", "json")
	}
	want := [][]string{
		head(publishKey),
		withRegion("s3", "cp", filepath.Join(h.root, "pull", publishOutput+".qcow2"), object, "--only-show-errors"),
		head(publishKey),
		withRegion("s3", "cp", object, "-", "--only-show-errors"),
		head(publishKey + ".sha256"),
		withRegion("s3", "cp", "-", object+".sha256", "--only-show-errors"),
		withRegion("s3", "cp", object+".sha256", "-", "--only-show-errors"),
	}
	if got := h.awsCalls(); !reflect.DeepEqual(got, want) {
		t.Errorf("object store calls:\n%v\nwant:\n%v", got, want)
	}
}

func TestPublishRunsEveryToolWithoutAPager(t *testing.T) {
	envLog := filepath.Join(t.TempDir(), "env.log")
	h := newPublishHarness(t, withAWS(func(c *fakeAWSConfig) { c.EnvLog = envLog }))
	h.run(nil).succeeds(t)
	data, err := os.ReadFile(envLog)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 7 {
		t.Fatalf("%d object store calls saw the environment, want 7", len(lines))
	}
	for _, line := range lines {
		if line != `true:""` {
			t.Errorf("AWS_PAGER = %s, want it set and empty", line)
		}
	}
}

func TestPublishRecordNamesWhatWasCheckedAndClaimsNoAuthority(t *testing.T) {
	h := newPublishHarness(t)
	h.run(nil).succeeds(t)
	location := "https://" + publishDomain + "/" + publishKey
	if !imageURLPattern.MatchString(location) {
		t.Errorf("%s is not a URL the node image check accepts", location)
	}
	source := func() map[string]any {
		return map[string]any{"kind": "nvkr-release", "reference": publishReference, "version": publishVersion}
	}
	want := map[string]any{
		"schema":                 "nvkr.dev/hardware-e2e-node-image-publication/v1",
		"slot":                   publishSlot,
		"qualificationAuthority": "none",
		"signedProjection":       false,
		"source":                 source(),
		"pulled": map[string]any{
			"output": publishOutput, "declaredArch": "amd64",
			"digest": "sha256:" + strings.Repeat("b", 64), "buildDigest": "sha256:" + strings.Repeat("c", 64),
		},
		"store": map[string]any{
			"bucket": publishBucket, "key": publishKey, "sidecarKey": publishKey + ".sha256", "region": "us-west-2",
			"contentLength": len(publishImage), "reusedExistingObject": false, "reusedExistingSidecar": false,
		},
		"qcow2": map[string]any{"version": 3, "virtualSizeBytes": 12 << 30, "clusterSizeBytes": 65536},
		"manifestEntry": map[string]any{
			"url": location, "sha256": publishDigest, "source": source(),
			"verification": map[string]any{
				"method": "s3-readback", "run": publishRun, "bytes": len(publishImage), "qcow2Version": 3, "virtualSizeBytes": 12 << 30,
			},
		},
	}
	got, err := os.ReadFile(filepath.Join(h.recordDir(), publishSlot+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if expected := canonicalFile(t, want); !bytes.Equal(got, expected) {
		t.Errorf("record:\n%s\nwant:\n%s", got, expected)
	}
}

func TestPublishManifestEntryIsWhatTheLaneAcceptsAndRecords(t *testing.T) {
	// The lane's node image check reads the same entry the maintainer pastes into the outputs manifest. If the two
	// ever disagree, a published image would be refused at dispatch.
	for _, slot := range []string{"os-image-amd64", "os-image-arm64"} {
		t.Run(slot, func(t *testing.T) {
			arch := strings.TrimPrefix(slot, "os-image-")
			h := newPublishHarness(t, withArch(arch))
			h.run(overrides{"--slot": slot}).succeeds(t)
			entry := h.record(slot)["manifestEntry"].(map[string]any)
			keys := make([]string, 0, len(entry))
			for key := range entry {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			if got := strings.Join(keys, ","); got != "sha256,source,url,verification" {
				t.Errorf("manifest entry keys = %s", got)
			}
			verified, err := parseVerification(entry["verification"])
			if err != nil {
				t.Fatalf("the lane refuses the verification: %v", err)
			}
			if want := (verification{method: "s3-readback", run: publishRun, bytes: int64(len(publishImage)), virtualBytes: 12 << 30, qcow2Version: 3}); verified != want {
				t.Errorf("verification = %+v, want %+v", verified, want)
			}
			substitutions := map[string]any{
				"schema": "nvkr.dev/hardware-e2e-substitutions/v1", "artifactSource": "nvkr-outputs",
				"replaced": []any{map[string]any{
					"slot": slot, "from": "https://nvcr.example.test/old.qcow2", "to": entry["url"],
					"sha256": entry["sha256"], "source": entry["source"], "verification": entry["verification"],
				}},
			}
			data, err := json.Marshal(substitutions)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(h.root, slot+"-substitutions.json")
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			url, digest, named, err := namedIn(path, slot)
			if err != nil {
				t.Fatalf("the lane refuses the substitution: %v", err)
			}
			if url != entry["url"] || digest != entry["sha256"] || named != verified {
				t.Errorf("namedIn = %s %s %+v, want the manifest entry", url, digest, named)
			}
			description, err := recorded(slot, url, digest, named)
			if err != nil {
				t.Fatal(err)
			}
			if description["run"] != publishRun || description["bytes"] != int64(len(publishImage)) || description["observedByThisRun"] != false {
				t.Errorf("recorded = %v", description)
			}
		})
	}
}

func TestPublishKeyHoldsTheDigestSoAnotherImageCannotLandOnIt(t *testing.T) {
	other := newQCOW2().image(payloadOf("another payload ", 64))
	first, second := newPublishHarness(t), newPublishHarness(t, withImage(other))
	first.run(nil).succeeds(t)
	second.run(nil).succeeds(t)
	firstKey := first.record(publishSlot)["store"].(map[string]any)["key"]
	secondKey := second.record(publishSlot)["store"].(map[string]any)["key"]
	if firstKey == secondKey {
		t.Error("two different images share a key")
	}
	if want := publishKeyFor("amd64", sumHex(other)); secondKey != want {
		t.Errorf("key = %v, want %s", secondKey, want)
	}
}

func TestPublishArm64SlotPublishesUnderItsOwnArchitecture(t *testing.T) {
	h := newPublishHarness(t, withArch("arm64"))
	h.run(overrides{"--slot": "os-image-arm64"}).succeeds(t)
	if got, want := h.record("os-image-arm64")["store"].(map[string]any)["key"], publishKeyFor("arm64", publishDigest); got != want {
		t.Errorf("key = %v, want %s", got, want)
	}
}

func TestPublishReusesWhatIsAlreadyThere(t *testing.T) {
	t.Run("a second publication of the same image reuses the object", func(t *testing.T) {
		h := newPublishHarness(t)
		h.seed(publishKey, publishImage)
		h.seed(publishKey+".sha256", []byte(publishDigest))
		got := h.run(nil).succeeds(t)
		if puts := h.uploaded(); len(puts) != 0 {
			t.Errorf("uploads %v, want none", puts)
		}
		store := h.record(publishSlot)["store"].(map[string]any)
		if store["reusedExistingObject"] != true || store["reusedExistingSidecar"] != true {
			t.Errorf("store = %v, want both reused", store)
		}
		containsAll(t, got.stdout, "reused the existing object")
	})

	t.Run("an object without its sidecar gets the sidecar once it reads back right", func(t *testing.T) {
		h := newPublishHarness(t)
		h.seed(publishKey, publishImage)
		h.run(nil).succeeds(t)
		if string(h.stored(publishKey+".sha256")) != publishDigest {
			t.Errorf("sidecar = %q, want %q", h.stored(publishKey+".sha256"), publishDigest)
		}
		if puts := h.uploaded(); !reflect.DeepEqual(puts, []string{"put-text"}) {
			t.Errorf("uploads = %v, want only the sidecar", puts)
		}
		store := h.record(publishSlot)["store"].(map[string]any)
		if store["reusedExistingObject"] != true || store["reusedExistingSidecar"] != false {
			t.Errorf("store = %v", store)
		}
	})
}

func TestPublishNeverReplacesWhatDiffers(t *testing.T) {
	t.Run("an existing object that differs", func(t *testing.T) {
		sameLength := append([]byte(nil), publishImage...)
		sameLength[len(sameLength)-1] ^= 1
		cases := []struct {
			name     string
			existing []byte
			fragment string
		}{
			{"different bytes", sameLength, "reads back as sha256"},
			{"another length", append(append([]byte(nil), publishImage...), 'x'), "bytes in the object store"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				h := newPublishHarness(t)
				h.seed(publishKey, tc.existing)
				h.run(nil).fails(t, 47, tc.fragment)
				if !bytes.Equal(h.stored(publishKey), tc.existing) {
					t.Error("the existing object was replaced")
				}
				h.untouched()
			})
		}
	})

	t.Run("an existing sidecar that differs", func(t *testing.T) {
		h := newPublishHarness(t)
		h.seed(publishKey, publishImage)
		h.seed(publishKey+".sha256", []byte(strings.Repeat("f", 64)))
		h.run(nil).fails(t, 47, "holds")
		if string(h.stored(publishKey+".sha256")) != strings.Repeat("f", 64) {
			t.Error("the existing sidecar was replaced")
		}
		if h.recordExists() {
			t.Error("a record was written")
		}
	})
}

func TestPublishStoreThatDoesNotHoldTheUploadedBytesIsAFailure(t *testing.T) {
	cases := []struct {
		name     string
		edit     func(*fakeAWSConfig)
		fragment string
	}{
		{"corrupted bytes", func(c *fakeAWSConfig) { c.Corrupt = true }, "reads back as sha256"},
		{"a shorter object", func(c *fakeAWSConfig) { c.Truncate = true }, "bytes in the object store"},
		{"an upload that went nowhere", func(c *fakeAWSConfig) { c.Drop = true }, "is missing from the object store"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newPublishHarness(t, withAWS(tc.edit))
			h.run(nil).fails(t, 22, tc.fragment)
			if h.recordExists() {
				t.Error("a record was written")
			}
		})
	}
}

func TestPublishStoreFailuresAreCodedAndLeaveNoRecord(t *testing.T) {
	failing := func(match, stderr string, code int) harnessOption {
		return withAWS(func(c *fakeAWSConfig) { c.Fail = []failRule{{Match: match, Stderr: stderr, Code: code}} })
	}
	cases := []struct {
		name      string
		option    harnessOption
		fragments []string
		ops       []string
	}{
		{"the first head", failing("head-object", "boom", 1), []string{"head-object for", "boom"}, []string{"head"}},
		{"the upload", failing("s3 cp ", "boom", 1), []string{"uploading", "boom"}, []string{"head", "put-file"}},
		{"the read-back", failing(" - --only-show-errors", "connection reset", 1), []string{"connection reset", "after 0 bytes"}, []string{"head", "put-file", "head", "get"}},
		{"the sidecar write", failing(".sha256 --only-show-errors", "denied", 1), []string{"uploading", "denied"}, []string{"head", "put-file", "head", "get", "head", "put-text"}},
		{"a forbidden head", failing("head-object", "An error occurred (403) when calling the HeadObject operation: Forbidden", 254), []string{"s3:ListBucket"}, []string{"head"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newPublishHarness(t, tc.option)
			got := h.run(nil)
			got.fails(t, 30, tc.fragments[0])
			containsAll(t, got.stderr, tc.fragments...)
			if ops := h.awsOps(); !reflect.DeepEqual(ops, tc.ops) {
				t.Errorf("object store calls = %v, want %v", ops, tc.ops)
			}
			if h.recordExists() {
				t.Error("a record was written")
			}
		})
	}
}

func TestPublishHeadAnswersItDoesNotUnderstand(t *testing.T) {
	for _, body := range []string{
		`{"ContentLength": "5"}`, `{}`, `[]`, `not json`, `{"ContentLength": -1}`, `{"ContentLength": 5.5}`,
		`{"ContentLength": true}`, `{"ContentLength": 1} trailing`,
	} {
		t.Run(fmt.Sprintf("%q", body), func(t *testing.T) {
			h := newPublishHarness(t, withAWS(func(c *fakeAWSConfig) { c.HeadBody = body }))
			h.run(nil).fails(t, 30, "head-object for "+publishKey+" answered something this script does not understand")
			if ops := h.awsOps(); !reflect.DeepEqual(ops, []string{"head"}) {
				t.Errorf("object store calls = %v, want only the head", ops)
			}
		})
	}
}

func TestPublishReadBackThatOutlivesItsDeadlineIsStopped(t *testing.T) {
	h := newPublishHarness(t, withAWS(func(c *fakeAWSConfig) { c.SlowSeconds = 30 }))
	limits := defaultPublishLimits
	limits.store = 2 * time.Second
	started := time.Now()
	h.runWith(context.Background(), limits, nil).fails(t, 31, "still running")
	if elapsed := time.Since(started); elapsed > 20*time.Second {
		t.Errorf("took %s: the slow tool was waited for, not stopped", elapsed)
	}
	if ops := h.awsOps(); !reflect.DeepEqual(ops, []string{"head", "put-file", "head", "get"}) {
		t.Errorf("object store calls = %v, want the read-back to be the call that was stopped", ops)
	}
	if h.recordExists() {
		t.Error("a record was written")
	}
}

func TestPublishInterruptedRunIsReportedAsInterrupted(t *testing.T) {
	t.Run("before anything runs", func(t *testing.T) {
		h := newPublishHarness(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		h.runWith(ctx, defaultPublishLimits, nil).fails(t, 31, "was interrupted")
		h.untouched()
	})

	t.Run("while the store is being read", func(t *testing.T) {
		h := newPublishHarness(t, withAWS(func(c *fakeAWSConfig) { c.SlowSeconds = 30 }))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		time.AfterFunc(1500*time.Millisecond, cancel)
		started := time.Now()
		h.runWith(ctx, defaultPublishLimits, nil).fails(t, 31, "was interrupted")
		if elapsed := time.Since(started); elapsed > 20*time.Second {
			t.Errorf("took %s: the slow tool was waited for, not stopped", elapsed)
		}
		if h.recordExists() {
			t.Error("a record was written")
		}
	})
}

func TestPublishDryRun(t *testing.T) {
	t.Run("pulls and checks but writes nothing", func(t *testing.T) {
		h := newPublishHarness(t)
		got := h.run(overrides{"--dry-run": "true", "--aws": "/nonexistent/aws", "--run-url": omit}).succeeds(t)
		decoded, err := decodeJSON([]byte(got.stdout))
		if err != nil {
			t.Fatalf("stdout is not one JSON value: %v\n%s", err, got.stdout)
		}
		want := map[string]any{
			"dryRun": true, "slot": publishSlot, "bytes": len(publishImage), "sha256": publishDigest, "key": publishKey,
			"url":   "https://" + publishDomain + "/" + publishKey,
			"qcow2": map[string]any{"version": 3, "virtualSizeBytes": 12 << 30, "clusterSizeBytes": 65536},
		}
		if expected := canonicalFile(t, want); got.stdout != string(expected) {
			t.Errorf("report:\n%s\nwant:\n%s", got.stdout, expected)
		}
		if _, isObject := decoded.(map[string]any); !isObject {
			t.Errorf("report = %v", decoded)
		}
		if calls := h.awsCalls(); len(calls) != 0 {
			t.Errorf("the object store was called: %v", calls)
		}
		if h.recordExists() {
			t.Error("a record was written")
		}
	})

	t.Run("still refuses an image it could not publish", func(t *testing.T) {
		image := newQCOW2().with(func(s *qcow2Spec) { s.backing = 4096 }).image(bytes.Repeat([]byte("x"), 64))
		h := newPublishHarness(t, withImage(image))
		h.run(overrides{"--dry-run": "true"}).fails(t, 13, "backing file")
	})

	t.Run("still refuses a pull that fails", func(t *testing.T) {
		h := newPublishHarness(t, withPullFailure(9, "no route"))
		h.run(overrides{"--dry-run": "true"}).fails(t, 20, "release pull exited 9: no route")
	})
}

func TestPublishRecordIsPrivateAndASecondWriteIsRefused(t *testing.T) {
	h := newPublishHarness(t)
	h.run(nil).succeeds(t)
	record := h.recordDir()
	dirInfo, err := os.Stat(record)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(record, publishSlot+".json")
	fileInfo, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 || fileInfo.Mode().Perm() != 0o600 {
		t.Errorf("record modes = %v and %v, want 0700 and 0600", dirInfo.Mode().Perm(), fileInfo.Mode().Perm())
	}
	before, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(h.root, "link")
	if err := os.Symlink(filepath.Join(h.root, "elsewhere"), link); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{record, link} {
		t.Run(filepath.Base(target), func(t *testing.T) {
			err := writeRecord(target, publishSlot, map[string]any{})
			assertCoded(t, err, nerrors.CodeImmutableViolation, "refusing to replace")
			if got := nerrors.ExitCode(err); got != 47 {
				t.Errorf("exit = %d, want 47", got)
			}
		})
	}
	if after, err := os.ReadFile(file); err != nil || !bytes.Equal(before, after) {
		t.Errorf("the record changed: %v", err)
	}
	if pathExists(filepath.Join(h.root, "elsewhere")) {
		t.Error("a write went through the symbolic link")
	}
}

func TestPublishRecordDirectoryThatCannotBeCreatedIsImmutableViolation(t *testing.T) {
	err := writeRecord(filepath.Join(t.TempDir(), "missing", "record"), publishSlot, map[string]any{})
	assertCoded(t, err, nerrors.CodeImmutableViolation, "could not create the record")
}

func TestCappedBuffer(t *testing.T) {
	buffer := &cappedBuffer{limit: 4}
	if n, err := buffer.Write([]byte("abcdef")); n != 6 || err != nil {
		t.Errorf("Write = %d, %v: a noisy tool must not be told its output was refused", n, err)
	}
	if n, err := buffer.Write([]byte("gh")); n != 2 || err != nil {
		t.Errorf("Write after the limit = %d, %v", n, err)
	}
	if got := buffer.text(); got != "abcd" {
		t.Errorf("text = %q, want the start of what was written", got)
	}

	messy := &cappedBuffer{limit: 16}
	_, _ = messy.Write([]byte{0xff, 'a', ' ', '\n'})
	if got := messy.text(); got != "\uFFFDa" {
		t.Errorf("text = %q, want the invalid byte replaced and the whitespace trimmed", got)
	}
}

func TestHashingWriter(t *testing.T) {
	hasher := &hashingWriter{digest: sha256.New()}
	for _, part := range []string{"hello ", "world"} {
		if n, err := hasher.Write([]byte(part)); n != len(part) || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	if got, want := hex.EncodeToString(hasher.digest.Sum(nil)), sumHex([]byte("hello world")); got != want || hasher.count != 11 {
		t.Errorf("digest = %s (want %s), count = %d", got, want, hasher.count)
	}
}
