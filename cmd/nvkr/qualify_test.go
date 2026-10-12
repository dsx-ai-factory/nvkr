// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
	"github.com/urfave/cli/v2"
)

func TestQualificationCLIMapsFilesWithoutTrustingVerdictFlags(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success diagnostic", true: "failure diagnostic"}[fail], func(t *testing.T) {
			var output bytes.Buffer
			app := &cli.App{Writer: &output, Commands: []*cli.Command{cmdRunQualifyWithPublisher(func(_ context.Context, options qualification.FilesOptions) (*qualification.Publication, error) {
				if options.ArtifactFiles.CandidatePath != "candidate.json" || options.ArtifactFiles.Source.RunID != "12" || options.QualificationWorkflow != ".github/workflows/qualify.yml" || options.Signer.CosignDigest != options.ArtifactFiles.Consumer.CosignDigest {
					t.Fatalf("incorrect mapping: %+v", options)
				}
				if len(options.Policy.OutsideMVPSuites) != 1 || options.Policy.OutsideMVPSuites[0] != "nvke" || len(options.Policy.RequiredSuites) != 0 {
					t.Fatal("suite policy changed")
				}
				result := &qualification.Publication{Successful: !fail, ArtifactChecks: &lifecycle.ArtifactCheckDescription{Outputs: []lifecycle.ArtifactCheckOutput{{Name: "cli", Overall: "fail", Checks: []lifecycle.ArtifactCheckResult{{Name: "build-signature-and-provenance", Status: "fail", Code: string(e.CodeSignatureInvalid)}}}}}}
				if fail {
					return result, e.New(e.CodeGateUnmet, "missing authenticated smoke")
				}
				return result, nil
			})}}
			args := []string{"nvkr", "qualify", "--definition", "definition.yaml", "--environment", "environment.yaml", "--plan", "plan.json", "--candidate", "candidate.json", "--source-repository", "example/nvkr", "--source-run", "12", "--source-attempt", "1", "--source-commit", strings.Repeat("a", 40), "--source-workflow", ".github/workflows/build.yml", "--default-branch", "main", "--cosign", "cosign", "--cosign-digest", "sha256:" + strings.Repeat("b", 64), "--qualification-workflow", ".github/workflows/qualify.yml", "--external-suite", "nvke"}
			err := app.Run(args)
			if fail {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != e.CodeGateUnmet {
					t.Fatalf("lost gate failure: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var result qualification.Publication
			if err := canonical.UnmarshalExact(output.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Successful == fail {
				t.Fatal("diagnostic success changed")
			}
			if result.ArtifactChecks == nil || len(result.ArtifactChecks.Outputs) != 1 || result.ArtifactChecks.Outputs[0].Checks[0].Code != string(e.CodeSignatureInvalid) {
				t.Fatal("artifact failure diagnostics were lost")
			}
		})
	}
	for _, flag := range cmdRunQualify().Flags {
		for _, name := range flag.Names() {
			if name == "passed" || name == "smoke-result" || name == "skip-smoke" || name == "allow-failed" {
				t.Fatalf("CLI exposes authority bypass %s", name)
			}
		}
	}
}
