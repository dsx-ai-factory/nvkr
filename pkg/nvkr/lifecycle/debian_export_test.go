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
			if len(transaction.stages) != 1 || artifact.Kind != OutputDebianPackage || artifact.Digest != pipeline.FileDigest(payload) || artifact.BuildDigest != slot.BuildDigest {
				t.Fatal("raw artifact acquired a wrapper or extra evidence stages")
			}
			if _, err := os.Stat(artifact.Reference); !errors.Is(err, os.ErrNotExist) {
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
				got, err := os.ReadFile(artifact.Reference)
				if err != nil || !bytes.Equal(got, payload) {
					t.Fatal("committed bytes changed", err)
				}
				info, err := os.Stat(artifact.Reference)
				if err != nil || info.Mode().Perm() != 0444 {
					t.Fatal("committed mode changed", err)
				}
			} else if _, err := os.Stat(artifact.Reference); !errors.Is(err, os.ErrNotExist) {
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
