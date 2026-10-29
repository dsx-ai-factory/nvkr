// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func rawPackageStageFixture(t *testing.T) (*diskExportTransaction, requiredOutput, *factory.Plan, []byte, string) {
	t.Helper()
	t.Chdir(t.TempDir())
	root := ".nvkr/attempt"
	component := catalog.Component{Platforms: []string{"linux/amd64"}, FromSource: true,
		BuilderType: ir.BuilderOCIHermetic, OutputType: "host-package", Repo: "https://example.test/source",
		Tag: "v1.0.0", Commit: strings.Repeat("a", 40), TreeHash: strings.Repeat("b", 40), SourceDate: 1,
		ToolchainImage: "example.test/toolchain@sha256:" + strings.Repeat("c", 64),
		BuildCommand:   []string{"make", "deb"}, ArtifactPath: "out/agent.deb", WriteDir: "out"}
	stack, err := catalog.ApplyStack(&catalog.Catalog{Components: map[string]catalog.Component{"agent": component}})
	if err != nil {
		t.Fatal(err)
	}
	d := authored.Definition{APIVersion: authored.DefinitionAPIVersion, Kind: authored.DefinitionKind,
		Metadata: authored.Metadata{Name: "raw-package"}, Catalog: authored.Catalog{Base: "catalog"},
		Outputs: []authored.DefinitionOutput{{Name: "agent", Platform: "linux/amd64", Format: OutputDebianPackage, Components: []string{"agent"}}}}
	env := authored.Environment{APIVersion: authored.EnvironmentAPIVersion, Kind: authored.EnvironmentKind,
		Metadata: authored.Metadata{Name: "local"}, UtilityContainer: component.ToolchainImage, UtilityPlatform: "linux/amd64"}
	plan, err := factory.Compile(t.Context(), d, env, stack, "test")
	if err != nil {
		t.Fatal(err)
	}
	output := plan.LifecycleOutputs[0]
	slot := requiredOutput{Name: output.Name, Kind: output.Kind, BuildDigest: output.BuildDigest,
		MemberArtifactID: output.ArtifactID, Components: output.Components}
	data := []byte("controlled raw producer bytes; not an actual Debian package")
	var producer ir.Node
	for _, node := range plan.Builds[0].Graph.Nodes {
		if node.Kind == ir.KindBuild {
			producer = node
		}
	}
	result := pipeline.ArtifactResult{Component: "agent", OutputType: "host-package", Qualification: pipeline.ArtifactUnqualified,
		Toolchain: producer.Toolchain, BuilderType: producer.BuilderType, InputDigest: "sha256:" + strings.Repeat("d", 64),
		Path: producer.ArtifactPath, SHA256: pipeline.FileDigest(data), Size: int64(len(data))}
	raw, err := canonical.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var recordPath string
	for _, artifact := range plan.Graph.Artifacts {
		if artifact.ID != output.ArtifactID && artifact.ID != "result/agent/agent" {
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(artifact.Path, ".nvkr/factory/")))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		contents := data
		if artifact.Kind == "producer-result" {
			contents, recordPath = raw, path
		}
		if err := os.WriteFile(path, contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return newDiskExportTransaction(root), slot, plan, data, recordPath
}

func TestRawPackageStagingUsesOneExistingTransaction(t *testing.T) {
	for _, mode := range []string{"commit", "rollback staged", "rollback committed"} {
		t.Run(mode, func(t *testing.T) {
			transaction, slot, plan, payload, _ := rawPackageStageFixture(t)
			artifact, err := transaction.observeDebian(t.Context(), slot, plan)
			if err != nil {
				t.Fatal(err)
			}
			if len(transaction.stages) != 1 || artifact.Kind != OutputDebianPackage || artifact.Digest != pipeline.FileDigest(payload) || artifact.BuildDigest != slot.BuildDigest || artifact.Reference != "" {
				t.Fatal("raw artifact acquired a wrapper or extra evidence stages")
			}
			path, err := LocalDebianOutputPath(transaction.workspace, artifact.Name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("staged output became visible", err)
			}
			if mode != "rollback staged" {
				if err := commitDiskExports(transaction.stages); err != nil {
					t.Fatal(err)
				}
			}
			if err := cleanupDiskExports(transaction.stages, mode == "commit"); err != nil {
				t.Fatal(err)
			}
			if mode == "commit" {
				got, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(got, payload) {
					t.Fatal("committed bytes changed", err)
				}
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0444 {
					t.Fatal("committed mode changed", err)
				}
			} else if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("rollback left committed output", err)
			}
			entries, err := os.ReadDir(filepath.Join(transaction.workspace, localDiskOutputDirectory))
			want := 0
			if mode == "commit" {
				want = 1
			}
			if err != nil || len(entries) != want {
				t.Fatal("transaction leaked temporary or evidence files", err)
			}
		})
	}
}

func TestRawPackageStageFailurePreservesExistingOutputAndRollsBack(t *testing.T) {
	for _, mode := range []string{"existing", "corrupt producer", "foreign slot", "canceled", "nil context", "traversal name", "nested name"} {
		t.Run(mode, func(t *testing.T) {
			transaction, slot, plan, _, recordPath := rawPackageStageFixture(t)
			path := filepath.Join(transaction.workspace, localDiskOutputDirectory, slot.Name+".deb")
			ctx := t.Context()
			switch mode {
			case "existing":
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("keep original"), 0600); err != nil {
					t.Fatal(err)
				}
			case "corrupt producer":
				if err := os.WriteFile(recordPath, []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "foreign slot":
				slot.BuildDigest = "sha256:" + strings.Repeat("e", 64)
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "nil context":
				ctx = nil
			case "traversal name":
				slot.Name = "../x"
			case "nested name":
				slot.Name = "a/b"
			}
			artifact, err := transaction.observeDebian(ctx, slot, plan)
			if err == nil || artifact.Name != "" {
				t.Fatal("invalid output was staged", err)
			}
			var coded *e.Error
			wantCode := e.CodeVerifyFailed
			if mode == "existing" {
				wantCode = e.CodeImmutableViolation
			}
			if mode == "canceled" {
				wantCode = e.CodeTimeout
			}
			if mode == "nil context" {
				wantCode = e.CodeUsage
			}
			if !errors.As(err, &coded) || coded.Code != wantCode {
				t.Fatalf("staging failure = %v, want %s", err, wantCode)
			}
			if (mode == "nil context" || mode == "traversal name" || mode == "nested name") && len(transaction.stages) != 0 {
				t.Fatal("invalid staging request performed output work")
			}
			if err := cleanupDiskExports(transaction.stages, false); err != nil {
				t.Fatal(err)
			}
			if mode == "existing" {
				contents, err := os.ReadFile(path)
				if err != nil || string(contents) != "keep original" {
					t.Fatal("replaced existing bytes", err)
				}
			} else if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("failure published raw output", err)
			}
		})
	}
}

func TestRawPackageObservationRequiresDurableStaging(t *testing.T) {
	artifact, err := observeOutput(t.Context(), requiredOutput{Name: "agent", Kind: OutputDebianPackage}, nil, nil, nil, t.TempDir())
	var coded *e.Error
	if artifact.Name != "" || !errors.As(err, &coded) || coded.Code != e.CodeEngineCapability {
		t.Fatalf("ordinary raw-package observation = %v, want %s and no artifact", err, e.CodeEngineCapability)
	}
}

func TestLocalDebianOutputPath(t *testing.T) {
	for _, test := range []struct {
		name, workspace, output, want string
		invalid                       bool
	}{
		{name: "relative", workspace: ".nvkr/runs/attempt", output: "agent", want: ".nvkr/runs/attempt/outputs/agent.deb"},
		{name: "absolute", workspace: t.TempDir(), output: "agent"},
		{name: "missing workspace", output: "agent", invalid: true},
		{name: "missing name", workspace: ".nvkr/runs/attempt", invalid: true},
		{name: "traversal", workspace: ".nvkr/runs/attempt", output: "../agent", invalid: true},
		{name: "nested", workspace: ".nvkr/runs/attempt", output: "nested/agent", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := LocalDebianOutputPath(test.workspace, test.output)
			if test.invalid {
				assertCode(t, err, e.CodeUsage)
				if got != "" {
					t.Fatalf("invalid output returned path %q", got)
				}
				return
			}
			want := test.want
			if want == "" {
				want = filepath.Join(test.workspace, "outputs", test.output+".deb")
			}
			if err != nil || got != want {
				t.Fatalf("local package path = %q, %v; want %q", got, err, want)
			}
		})
	}
}

func TestExecuteLocalRawPackageCommitsAndRollsBack(t *testing.T) {
	for _, mode := range []string{"complete", "record write failure", "linked record sync failure"} {
		t.Run(mode, func(t *testing.T) {
			fixture, slot, plan, payload, _ := rawPackageStageFixture(t)
			description, err := PrepareRun(plan.Definition, plan.Environment, plan, "package-attempt")
			if err != nil {
				t.Fatal(err)
			}
			prepared := &PreparedRun{plan: plan, run: description}
			workspace := filepath.Join(".nvkr", "runs", description.Attempt)
			path, err := LocalDebianOutputPath(workspace, slot.Name)
			if err != nil {
				t.Fatal(err)
			}
			exporter := &fakeDiskExporter{}
			factoryCalled, commitCalled, recordCalled := false, false, false
			cause := e.New(e.CodeImmutableViolation, "synthetic package record write failure")
			run, err := executeLocalWithDependencies(t.Context(), prepared,
				LocalOptions{AttemptsRoot: ".nvkr/runs"}, localExecutionDependencies{
					admitLocal: func(context.Context, *factory.Plan, factory.LocalRunOptions) error { return nil },
					runFactory: func(_ context.Context, got *factory.Plan, options factory.LocalRunOptions) (localExecutionResult, error) {
						factoryCalled = true
						if got != plan || options.Workspace != filepath.ToSlash(workspace) {
							t.Fatal("local executor received a different Plan or workspace")
						}
						if err := os.CopyFS(options.Workspace, os.DirFS(fixture.workspace)); err != nil {
							t.Fatal(err)
						}
						return exporter, nil
					},
					beginDiskCommit: func(workspace string) (*diskAttemptTransaction, error) {
						commitCalled = true
						return beginDiskAttemptTransaction(workspace)
					},
					writeRecordState: func(ctx context.Context, recordPath string, bound *Run) (bool, error) {
						recordCalled = true
						if bound.Status != RunComplete || len(bound.Observed) != 1 ||
							bound.Observed[0].Kind != OutputDebianPackage || bound.Observed[0].Reference != "" || bound.ContentSubject == "" {
							t.Fatalf("record did not bind the exact local package: %+v", bound)
						}
						if got, err := os.ReadFile(path); err != nil || !bytes.Equal(got, payload) {
							t.Fatalf("record write preceded raw package commit: %q, %v", got, err)
						}
						if _, err := os.Stat(filepath.Join(workspace, diskAttemptMarker)); err != nil {
							t.Fatalf("record write has no attempt transaction marker: %v", err)
						}
						switch mode {
						case "record write failure":
							return false, cause
						case "linked record sync failure":
							return writeRunRecordStateWithSync(ctx, recordPath, bound, func(*os.File) error { return cause })
						default:
							return writeRunRecordState(ctx, recordPath, bound)
						}
					},
				})
			if !factoryCalled || !commitCalled || !recordCalled || !exporter.closed || exporter.exports != 0 {
				t.Fatalf("raw local transaction ownership: factory=%t commit=%t record=%t closed=%t diskExports=%d",
					factoryCalled, commitCalled, recordCalled, exporter.closed, exporter.exports)
			}
			wantStatus, wantOutputs := RunComplete, 1
			if mode == "complete" {
				if err != nil {
					t.Fatal(err)
				}
				info, statErr := os.Stat(path)
				if statErr != nil || info.Mode().Perm() != 0444 {
					t.Fatalf("committed package is not read-only: %v", statErr)
				}
			} else {
				wantStatus, wantOutputs = RunFailed, 0
				assertCode(t, err, e.CodeImmutableViolation)
				if !errors.Is(err, cause) {
					t.Fatalf("record failure lost its cause: %v", err)
				}
				if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatalf("record failure left committed package: %v", statErr)
				}
			}
			if run == nil || run.Status != wantStatus || run.Lane != factory.TierLocalExecution.String() || len(run.Observed) != wantOutputs {
				t.Fatalf("local package Run = %+v, want %s with %d outputs", run, wantStatus, wantOutputs)
			}
			if _, err := os.Stat(filepath.Join(workspace, diskAttemptMarker)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("local transaction marker remained: %v", err)
			}
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil || len(entries) != wantOutputs {
				t.Fatalf("raw package transaction leaked output files: %v, %v", entries, err)
			}
			record, err := os.ReadFile(filepath.Join(workspace, "lifecycle.json"))
			if err != nil || !bytes.Contains(record, []byte(`"status":"`+wantStatus+`"`)) || bytes.Contains(record, []byte(`"reference"`)) {
				t.Fatalf("durable raw package Run record = %q, %v", record, err)
			}
		})
	}
}
