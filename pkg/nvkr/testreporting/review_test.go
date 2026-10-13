// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testreporting

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
	"golang.org/x/sys/unix"
)

func rebound(t *testing.T, in *Input, r *fakeReceipt) {
	t.Helper()
	d, err := r.observed.Digest()
	if err != nil {
		t.Fatal(err)
	}
	r.description.ObservationDigest = d
	in.Expected.ObservationDigest = d
	in.Locator.ObservationDigest = d
}
func canonicalFile(t *testing.T, root, name string, value any) string {
	t.Helper()
	raw, err := canonical.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, name)
	if err = os.WriteFile(p, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func reportFixture(t *testing.T, in Input, r receipt) *Report {
	t.Helper()
	report, err := ingestWith(t.Context(), []Input{in}, testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}}, func(context.Context, Input, testobservation.RegistryOptions) (receipt, error) { return r, nil })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = report.Close() })
	return report
}

func TestExportFilesCompleteAdmissionAndCommittedPaths(t *testing.T) {
	for _, kind := range []string{"json", "markdown", "grid", "missing-start", "reversed-times", "outside-membership", "existing-output", "existing-grid", "invalid-target"} {
		t.Run(kind, func(t *testing.T) {
			in, r := fixture(t, "adapter-review", "pass", "workflow/main", FirstParty)
			root := t.TempDir()
			out := filepath.Join(root, "report")
			gridOut := filepath.Join(root, "grid")
			d := destinationFor(in)
			format := "json"
			target := ""
			switch kind {
			case "markdown":
				format = "markdown"
			case "missing-start":
				for i, f := range r.observed.Environment.Facts {
					if f.Name == "testStartedUnix" {
						r.observed.Environment.Facts = append(r.observed.Environment.Facts[:i], r.observed.Environment.Facts[i+1:]...)
						break
					}
				}
				rebound(t, &in, r)
				d = destinationFor(in)
			case "reversed-times":
				for i, f := range r.observed.Environment.Facts {
					if f.Name == "testFinishedUnix" {
						r.observed.Environment.Facts[i].Value = "1700000000"
					}
				}
				rebound(t, &in, r)
				d = destinationFor(in)
			case "outside-membership":
				d.ObservationDigest = pin("other")
			case "existing-output":
				if err := os.WriteFile(out, []byte("existing"), 0600); err != nil {
					t.Fatal(err)
				}
			case "existing-grid":
				if err := os.Mkdir(gridOut, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "json" && kind != "markdown" && kind != "existing-output" {
				target = canonicalFile(t, root, "target.json", d)
			} else {
				gridOut = ""
			}
			if kind == "invalid-target" {
				if err := os.WriteFile(target, []byte(`{"unexpected":true}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			inputs := canonicalFile(t, root, "inputs.json", []Input{in})
			calls := 0
			result, err := exportFilesWith(t.Context(), inputs, format, out, target, gridOut, testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}}, func(context.Context, Input, testobservation.RegistryOptions) (receipt, error) { calls++; return r, nil })
			if kind == "json" || kind == "markdown" || kind == "grid" {
				if err != nil || result == nil || !result.Complete || !result.ReportCommitted || result.Output != out || result.Published {
					t.Fatalf("result=%+v err=%v", result, err)
				}
				if kind == "grid" && (!result.TestGridCommitted || result.TestGridOutput != gridOut) {
					t.Fatalf("grid result=%+v", result)
				}
				return
			}
			want := e.CodeVerifyFailed
			if kind == "missing-start" {
				want = e.CodeGateUnmet
			}
			if strings.HasPrefix(kind, "existing-") {
				want = e.CodeImmutableViolation
			}
			code(t, err, want)
			if result != nil {
				t.Fatalf("admission returned committed paths: %+v", result)
			}
			if kind != "existing-output" {
				if _, err = os.Lstat(out); !os.IsNotExist(err) {
					t.Fatal("known admission failure wrote report")
				}
			}
			if kind == "invalid-target" && calls != 0 {
				t.Fatal("invalid target reached verification")
			}
		})
	}
}

func TestSourceConsistencyBeforeAuthentication(t *testing.T) {
	for _, kind := range []string{"first-party-community-path", "tier", "signer", "definition", "signer-padding"} {
		t.Run(kind, func(t *testing.T) {
			in, r := fixture(t, "consistent", "pass", "workflow/main", FirstParty)
			second := cloneInput(in)
			switch kind {
			case "first-party-community-path":
				in.Admission.SourcePath = "community/test"
				in.Locator.Reference = in.Admission.Namespace + "/" + in.Admission.SourcePath + "@" + pin("transport")
				second = in
			case "tier":
				second.Admission.Tier = Community
			case "signer":
				second.Admission.Identity = "https://other.example/signer"
			case "signer-padding":
				second.Admission.Identity = " padded "
			case "definition":
				second.Admission.DefinitionDigest = pin("other-definition")
				second.Expected.Inputs.DefinitionDigest = second.Admission.DefinitionDigest
			}
			calls := 0
			got, err := ingestWith(t.Context(), []Input{in, second}, testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}}, func(context.Context, Input, testobservation.RegistryOptions) (receipt, error) { calls++; return r, nil })
			code(t, err, e.CodeVerifyFailed)
			if got != nil || calls != 0 {
				t.Fatal("conflicting sources reached authentication")
			}
		})
	}
}

func TestSignerGroupingFamilyAndMarkdownFidelity(t *testing.T) {
	in, r := fixture(t, "grouping", "pass", "unused", Community)
	path, err := ProjectionSourcePath(in.Admission.DefinitionDigest, r.identity, r.issuer)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := canonical.Digest(canonical.KindCommunitySignerLabel, struct {
		Identity string `json:"identity"`
		Issuer   string `json:"issuer"`
	}{r.identity, r.issuer})
	if err != nil || !strings.HasSuffix(path, strings.TrimPrefix(digest, "sha256:")) {
		t.Fatal("grouping lacks registered domain", err)
	}
	for _, tt := range []struct{ raw, want string }{{`a"b`, `a"b`}, {`a\b`, `a\\b`}, {"a\tb", `a\u0009b`}, {"a\nb", `a\u000ab`}, {"a\u202eb", `a\u202eb`}, {"[link]", `\[link\]`}} {
		if got := markdown(tt.raw); got != tt.want {
			t.Fatalf("%q=>%q want%q", tt.raw, got, tt.want)
		}
	}
}

func TestCommitFailureCodesAndPartialState(t *testing.T) {
	for _, tt := range []struct {
		err  error
		want e.Code
	}{{unix.EEXIST, e.CodeImmutableViolation}, {unix.ENOTEMPTY, e.CodeImmutableViolation}, {unix.ENOTSUP, e.CodeEngineCapability}, {unix.EINVAL, e.CodeEngineCapability}, {unix.EIO, e.CodeVerifyFailed}} {
		code(t, renameFailure(tt.err, "commit"), tt.want)
	}
	code(t, committedFailure(t.Context(), unix.EIO, "committed; no rollback performed"), e.CodeVerifyFailed)
	in, r := fixture(t, "post-commit", "pass", "workflow/main", FirstParty)
	report := reportFixture(t, in, r)
	raw, err := report.JSON(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "report.json")
	ctx, cancel := context.WithCancel(t.Context())
	committed := false
	err = report.exportLocalFileWithCommit(ctx, out, raw, func() { committed = true; cancel() })
	code(t, err, e.CodeTimeout)
	if !committed || !strings.Contains(err.Error(), "no rollback performed") {
		t.Fatal("lost explicit partial commit", err)
	}
	got, readErr := os.ReadFile(out)
	if readErr != nil || string(got) != string(raw) {
		t.Fatal("committed file missing", readErr)
	}
}

type reviewHookReceipt struct {
	*fakeReceipt
	hook func()
}

func (r *reviewHookReceipt) Description(ctx context.Context) (testobservation.Verification, error) {
	if r.hook != nil {
		r.hook()
	}
	return r.fakeReceipt.Description(ctx)
}
func TestOutputReplacementChecksAndGridPartialCommit(t *testing.T) {
	for _, kind := range []string{"parent", "stage", "grid-parent", "grid-stage", "grid-cancel"} {
		t.Run(kind, func(t *testing.T) {
			in, r := fixture(t, "replacement", "pass", "workflow/main", FirstParty)
			root := t.TempDir()
			parent := filepath.Join(root, "parent")
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(parent, "output")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			hook := &reviewHookReceipt{fakeReceipt: r}
			report := reportFixture(t, in, hook)
			fired := false
			hook.hook = func() {
				if fired {
					return
				}
				entries, _ := os.ReadDir(parent)
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".nvkr-") {
						fired = true
						switch kind {
						case "parent", "grid-parent":
							if err := os.Rename(parent, parent+"-old"); err != nil {
								t.Fatal(err)
							}
							if err := os.Mkdir(parent, 0700); err != nil {
								t.Fatal(err)
							}
						case "stage", "grid-stage":
							name := filepath.Join(parent, entry.Name())
							if err := os.Rename(name, name+"-old"); err != nil {
								t.Fatal(err)
							}
							if kind == "stage" {
								if err := os.WriteFile(name, []byte("replacement"), 0600); err != nil {
									t.Fatal(err)
								}
							} else {
								if err := os.Mkdir(name, 0700); err != nil {
									t.Fatal(err)
								}
							}
						case "grid-cancel":
							cancel()
						}
						return
					}
				}
			}
			var err error
			if strings.HasPrefix(kind, "grid-") {
				err = report.ExportTestGrid(ctx, destinationFor(in), out)
			} else {
				err = report.ExportIndex(ctx, out)
			}
			want := e.CodeVerifyFailed
			if kind == "grid-cancel" {
				want = e.CodeTimeout
			}
			code(t, err, want)
			if !fired {
				t.Fatal("replacement seam not reached")
			}
			if _, err = os.Lstat(out); !os.IsNotExist(err) {
				t.Fatal("replaced output committed")
			}
		})
	}
}

func TestTestGridMetadataExampleAndModes(t *testing.T) {
	in, r := fixture(t, "example", "pass", "workflow/main", FirstParty)
	report := reportFixture(t, in, r)
	projection, err := report.TestGrid(t.Context(), destinationFor(in))
	if err != nil {
		t.Fatal(err)
	}
	var header struct {
		Columns []struct {
			Key string `json:"configuration_value"`
		} `json:"column_header"`
	}
	raw, err := os.ReadFile("testdata/testgrid-column-header-example.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &header); err != nil {
		t.Fatal(err)
	}
	var start started
	for _, f := range projection.Files {
		if f.Name == "started.json" {
			if err = json.Unmarshal(f.Bytes, &start); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, column := range header.Columns {
		if _, ok := start.Metadata[column.Key]; !ok {
			t.Fatal("example metadata drift", column.Key)
		}
	}
	out := filepath.Join(t.TempDir(), "grid")
	if err = report.ExportTestGrid(t.Context(), destinationFor(in), out); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{out, filepath.Join(out, "artifacts")} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatal("directory mode", info, err)
		}
	}
	code(t, TestGridDestination{Bucket: "bucket", Prefix: "safe/but/wrong", Namespace: in.Admission.Namespace, SourcePath: in.Admission.SourcePath, ObservationDigest: in.Expected.ObservationDigest}.Validate(), e.CodeUsage)
}

func TestHeldFileBudgetClosesExcess(t *testing.T) {
	var inputs []Input
	var receipts []*fakeReceipt
	for n := 0; n < 5; n++ {
		in, r := fixture(t, string(rune('a'+n)), "pass", "workflow/main", FirstParty)
		for i := 1; i < 255; i++ {
			name := strings.Repeat("x", i/26+1) + string(rune('a'+i%26))
			r.observed.Files = append(r.observed.Files, testobservation.File{Name: name, Role: "fixture", Digest: pin(name), Size: 1})
		}
		slices.SortFunc(r.observed.Files, func(a, b testobservation.File) int { return strings.Compare(a.Name, b.Name) })
		rebound(t, &in, r)
		inputs = append(inputs, in)
		receipts = append(receipts, r)
	}
	calls := 0
	got, err := ingestWith(t.Context(), inputs, testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}}, verifier(inputs, receipts, &calls))
	code(t, err, e.CodeVerifyFailed)
	if got != nil || calls != 3 || !receipts[2].closed.Load() {
		t.Fatal("descriptor excess retained")
	}
	for i, r := range receipts {
		if r.closed.Load() != (i < 3) {
			t.Fatal("failure retained receipts")
		}
	}
}

func TestReportStagePrefixAndRecursiveCleanup(t *testing.T) {
	parent, _, err := securepath.OpenDirectory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer parent.Close()
	stage, name, err := securepath.CreateTemporarySiblingWithPrefix(parent, ".nvkr-report-")
	if err != nil {
		t.Fatal(err)
	}
	defer stage.Close()
	if !strings.HasPrefix(name, ".nvkr-report-") {
		t.Fatal(name)
	}
	for _, bad := range []string{"", "../escape", "a/b", "..", "bad\nlabel"} {
		_, _, err := securepath.CreateTemporarySiblingWithPrefix(parent, bad)
		if err == nil {
			t.Fatal("unsafe prefix")
		}
	}
	root := t.TempDir()
	if err = os.MkdirAll(filepath.Join(root, "extra", "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "extra", "nested", "file"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	held, _, err := securepath.OpenDirectory(root)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err = securepath.ClearDirectory(t.Context(), held); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 0 {
		t.Fatal("private stage debris retained")
	}
}

func TestGridDefensiveSelectionAndExactTransportOwner(t *testing.T) {
	in, r := fixture(t, "owner", "pass", "workflow/main", FirstParty)
	report := reportFixture(t, in, r)
	index, err := report.Index(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	duplicate := index
	duplicate.Entries = append(append([]Entry(nil), index.Entries...), index.Entries[0])
	_, err = selectGridEntry(duplicate, destinationFor(in))
	code(t, err, e.CodeVerifyFailed)
	_, err = (&Report{}).gridOwner(index.Entries[0])
	code(t, err, e.CodeVerifyFailed)
	different := index.Entries[0]
	different.Reference = in.Admission.Namespace + "/" + in.Admission.SourcePath + "@" + pin("other-transport")
	_, err = report.gridOwner(different)
	code(t, err, e.CodeVerifyFailed)
}
func TestExportFilesReturnsOnlyActualPartialCommit(t *testing.T) {
	in, r := fixture(t, "partial", "pass", "workflow/main", FirstParty)
	root := t.TempDir()
	inputs := canonicalFile(t, root, "inputs.json", []Input{in})
	target := canonicalFile(t, root, "target.json", destinationFor(in))
	out := filepath.Join(root, "report.json")
	grid := filepath.Join(root, "grid")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	hook := &reviewHookReceipt{fakeReceipt: r}
	hook.hook = func() {
		entries, _ := os.ReadDir(root)
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".nvkr-testgrid-") {
				cancel()
				return
			}
		}
	}
	result, err := exportFilesWith(ctx, inputs, "json", out, target, grid, testobservation.RegistryOptions{Transport: delivery.RegistryOptions{Anonymous: true}}, func(context.Context, Input, testobservation.RegistryOptions) (receipt, error) { return hook, nil })
	code(t, err, e.CodeTimeout)
	if result == nil || result.Output != out || !result.ReportCommitted || result.TestGridOutput != "" || result.TestGridCommitted || result.Complete || result.Published {
		t.Fatalf("partial=%+v", result)
	}
	if _, err = os.Stat(out); err != nil {
		t.Fatal("committed report absent", err)
	}
	if _, err = os.Stat(grid); !os.IsNotExist(err) {
		t.Fatal("uncommitted grid present")
	}
}

func TestGridStageOpenFailureDoesNotLeakDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses source-directory mode admission")
	}
	in, r := fixture(t, "stage-open", "pass", "workflow/main", FirstParty)
	report := reportFixture(t, in, r)
	parent := t.TempDir()
	out := filepath.Join(parent, "grid")
	previous := unix.Umask(0777)
	err := report.ExportTestGrid(t.Context(), destinationFor(in), out)
	unix.Umask(previous)
	code(t, err, e.CodeVerifyFailed)
	entries, readErr := os.ReadDir(parent)
	if readErr != nil || len(entries) != 0 {
		t.Fatalf("failed open leaked stage: %v %v", entries, readErr)
	}
}
func TestGridPostCommitCancellationNamesPartialState(t *testing.T) {
	in, r := fixture(t, "grid-postcommit", "pass", "workflow/main", FirstParty)
	report := reportFixture(t, in, r)
	projection, err := report.TestGrid(t.Context(), destinationFor(in))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	committed := false
	out := filepath.Join(t.TempDir(), "grid")
	err = report.exportGridProjection(ctx, projection, out, func() { committed = true; cancel() })
	code(t, err, e.CodeTimeout)
	if !committed || !strings.Contains(err.Error(), "no rollback performed") {
		t.Fatal("partial state lost", err)
	}
	if _, err = os.Stat(filepath.Join(out, "finished.json")); err != nil {
		t.Fatal("committed terminal marker lost", err)
	}
}
