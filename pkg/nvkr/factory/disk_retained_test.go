// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func TestObserveRetainedDiskPayload(t *testing.T) {
	for _, tc := range []struct {
		name string
		code e.Code
		edit func(*LifecycleOutputPlan, *pipeline.ArtifactResult, []byte)
	}{
		{"exact", "", func(*LifecycleOutputPlan, *pipeline.ArtifactResult, []byte) {}},
		{"different output", e.CodePipelineDrift, func(w *LifecycleOutputPlan, _ *pipeline.ArtifactResult, _ []byte) { w.Name = "sibling" }},
		{"different build", e.CodePipelineDrift, func(w *LifecycleOutputPlan, _ *pipeline.ArtifactResult, _ []byte) {
			w.BuildDigest = "sha256:" + strings.Repeat("9", 64)
		}},
		{"different toolchain", e.CodeVerifyFailed, func(_ *LifecycleOutputPlan, r *pipeline.ArtifactResult, _ []byte) { r.Toolchain = "other" }},
		{"changed bytes", e.CodeVerifyFailed, func(_ *LifecycleOutputPlan, _ *pipeline.ArtifactResult, p []byte) { p[len(p)-1] ^= 1 }},
		{"different subject", e.CodeVerifyFailed, func(_ *LifecycleOutputPlan, r *pipeline.ArtifactResult, _ []byte) {
			r.SHA256 = "sha256:" + strings.Repeat("9", 64)
		}},
		{"different size", e.CodeVerifyFailed, func(_ *LifecycleOutputPlan, r *pipeline.ArtifactResult, _ []byte) { r.Size++ }},
		{"unsupported framing with matching digest", e.CodeVerifyFailed, func(_ *LifecycleOutputPlan, r *pipeline.ArtifactResult, p []byte) {
			p[0] = 'X'
			r.SHA256 = pipeline.FileDigest(p)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan, want, completion := diskObservationFixture(t, "none")
			payload := testQCOW2HeaderPayload()
			tc.edit(&want, &completion.Result, payload)
			root := t.TempDir()
			payloadPath, resultPath := filepath.Join(root, "retained.qcow2"), filepath.Join(root, "result.json")
			if err := os.WriteFile(payloadPath, payload, 0o600); err != nil {
				t.Fatal(err)
			}
			raw, err := canonical.Marshal(completion.Result)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(resultPath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := ObserveRetainedDiskPayload(t.Context(), plan, want, payloadPath, resultPath)
			if tc.code != "" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != tc.code {
					t.Fatalf("error=%v want %s", err, tc.code)
				}
				if got.Digest != "" {
					t.Fatal("rejection returned a payload observation")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Digest != completion.Result.SHA256 || got.Size != completion.Result.Size || got.PlanDigest != completion.PlanDigest ||
				got.Producer != "build-image" || got.Format != "qcow2" || got.Encryption != "none" || got.Outcome != "" {
				t.Fatalf("retained observation changed its binding or inferred execution success: %+v", got)
			}
		})
	}
}

func TestObserveRetainedDiskSubjectWithoutProducerFile(t *testing.T) {
	for _, mode := range []string{"exact", "producer", "component", "plan", "build", "components", "format", "encryption", "outcome", "digest", "size", "bytes", "framing", "missing", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			plan, slot, completion := diskObservationFixture(t, "none")
			subject := DiskPayloadObservation{Digest: completion.Result.SHA256, Size: completion.Result.Size,
				BuildDigest: slot.BuildDigest, Components: append([]string(nil), slot.Components...),
				Producer: "build-image", Component: "image", Format: "qcow2", Encryption: "none", PlanDigest: completion.PlanDigest}
			payload := testQCOW2HeaderPayload()
			switch mode {
			case "producer":
				subject.Producer = "sibling"
			case "component":
				subject.Component = "sibling"
			case "plan":
				subject.PlanDigest = "sha256:" + strings.Repeat("9", 64)
			case "build":
				subject.BuildDigest = "sha256:" + strings.Repeat("9", 64)
			case "components":
				subject.Components = []string{"sibling"}
			case "format":
				subject.Format = "raw"
			case "encryption":
				subject.Encryption = "luks2"
			case "outcome":
				subject.Outcome = "succeeded"
			case "digest":
				subject.Digest = "sha256:" + strings.Repeat("9", 64)
			case "size":
				subject.Size++
			case "bytes":
				payload[len(payload)-1] ^= 1
			case "framing":
				payload[0] = 'X'
				subject.Digest = pipeline.FileDigest(payload)
			}
			path := filepath.Join(t.TempDir(), "disk.qcow2")
			if mode != "missing" {
				if err := os.WriteFile(path, payload, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "symlink" {
				link := path + ".link"
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			}
			got, err := ObserveRetainedDiskSubject(t.Context(), plan, slot, path, subject)
			if mode != "exact" {
				assertFactoryErrorCode(t, err, e.CodeVerifyFailed)
				if got.Digest != "" {
					t.Fatal("refusal returned observation")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Digest != subject.Digest || got.Outcome != "" {
				t.Fatal("wrong subject or invented execution authority")
			}
		})
	}
}
