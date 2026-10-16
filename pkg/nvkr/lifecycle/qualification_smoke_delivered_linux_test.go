// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package lifecycle

import (
	"context"
	"encoding/json"
	"os"
	"runtime"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

// Opt-in real delivered-byte execution. The external manifest supplies measured
// payload identities and independently selected producer expectations. This test
// grants no build/provenance receipt or complete-attempt qualification claim.
func TestDeliveredToolsRealSmoke(t *testing.T) {
	manifest := os.Getenv("NVKR_TEST_DELIVERED_TOOLS")
	if manifest == "" {
		t.Skip("requires explicit delivered tool manifest and pinned smoke image")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Minute)
	defer cancel()
	file, err := heldfile.OpenContext(ctx, manifest, heldfile.Options{Label: "delivered-tool-manifest", MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	raw, err := file.ReadAllContext(ctx, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var tools []struct {
		Path   string
		Member ToolSmokeMember
	}
	if err := json.Unmarshal(raw, &tools); err != nil || len(tools) == 0 {
		t.Fatalf("invalid tool manifest: %v", err)
	}
	owner, err := pipeline.OpenToolSmokeContainer(ctx, pipeline.ToolSmokeSelection{RuntimeExecutable: os.Getenv("NVKR_TEST_TOOL_SMOKE_DOCKER"), Image: os.Getenv("NVKR_TEST_TOOL_SMOKE_IMAGE"), Platform: "linux/" + runtime.GOARCH})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	})
	for _, tool := range tools {
		t.Run(tool.Member.Member, func(t *testing.T) {
			subject, err := heldfile.OpenContext(ctx, tool.Path, heldfile.Options{Label: "delivered-tool", MaxBytes: pipeline.MaxToolSmokeExecutableBytes})
			if err != nil {
				t.Fatal(err)
			}
			defer subject.Close()
			if subject.Digest() != tool.Member.PayloadDigest || subject.Size() != tool.Member.Size {
				t.Fatal("payload differs from selected manifest")
			}
			profile, err := nativeSmokeProfile(tool.Member.Profile)
			if err != nil {
				t.Fatal(err)
			}
			observation, err := owner.RunHeld(ctx, subject, profile)
			if err != nil {
				t.Fatal(err)
			}
			evaluated, err := evaluateNativeSmokeMember(tool.Member, observation)
			if err != nil {
				t.Logf("stdout=%q stderr=%q", observation.Stdout(), observation.Stderr())
				t.Fatal(err)
			}
			t.Logf("payload=%s bytes=%d version=%s commit=%s image=%s runtime=%s", subject.Digest(), subject.Size(), evaluated.ObservedVersion, evaluated.ObservedCommit, observation.Image(), observation.RuntimeIdentity())
			wrong := tool.Member
			wrong.ExpectedVersion = "invalid-version-expectation"
			_, err = evaluateNativeSmokeMember(wrong, observation)
			assertCode(t, err, e.CodeVerifyFailed)
		})
	}
}
