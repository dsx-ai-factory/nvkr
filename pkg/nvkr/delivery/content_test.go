// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestContentDigestPreservesFrozenCanonicalIdentity(t *testing.T) {
	// Independently calculated SHA-256 over the frozen nvkr.release-content/v1
	// prefix, NUL separator and canonical {artifacts:[...]} bytes. These exact
	// fields match the pre-consolidation producer and consumer representations.
	outputs := []Output{
		{Name: "tools", Kind: "executable", Digest: "sha256:" + strings.Repeat("c", 64), BuildDigest: "sha256:" + strings.Repeat("d", 64), Components: []string{"cli"}},
		{Name: "chart", Kind: "helm-oci", Digest: "sha256:" + strings.Repeat("a", 64), BuildDigest: "sha256:" + strings.Repeat("b", 64)},
	}
	for _, withReferences := range []bool{false, true} {
		if withReferences {
			outputs[0].Reference = "registry.example.test/tools@" + outputs[0].Digest
			outputs[1].Reference = "registry.example.test/chart@" + outputs[1].Digest
		}
		got, err := ContentDigest(outputs)
		if err != nil {
			t.Fatal(err)
		}
		if got != "sha256:c38a68523727250cd11bcaaa04a4f232d9faf69b568c79110b9713ccd43fdc21" {
			t.Fatalf("content canonical bytes changed: %s", got)
		}
		if outputs[0].Name != "tools" || outputs[0].Components[0] != "cli" || (withReferences && outputs[0].Reference == "") {
			t.Fatal("content digest mutated the caller's projection")
		}
	}
}

func TestContentDigestRejectsAmbiguousOutputs(t *testing.T) {
	valid := Output{Name: "tools", Kind: "executable", Digest: "sha256:" + strings.Repeat("a", 64), BuildDigest: "sha256:" + strings.Repeat("b", 64), Components: []string{"cli"}}
	for _, tc := range []struct {
		name      string
		change    func(*Output)
		duplicate bool
		code      e.Code
	}{
		{name: "duplicate output", duplicate: true, code: e.CodeVerifyFailed},
		{name: "missing build", change: func(o *Output) { o.BuildDigest = "" }, code: e.CodeVerifyFailed},
		{name: "unsafe component", change: func(o *Output) { o.Components = []string{"../cli"} }, code: e.CodeVerifyFailed},
		{name: "duplicate component", change: func(o *Output) { o.Components = []string{"cli", "cli"} }, code: e.CodeVerifyFailed},
		{name: "missing digest", change: func(o *Output) { o.Digest = "" }, code: e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := valid
			if tc.change != nil {
				tc.change(&output)
			}
			outputs := []Output{output}
			if tc.duplicate {
				outputs = append(outputs, output)
			}
			_, err := ContentDigest(outputs)
			assertRegistryCode(t, err, tc.code)
		})
	}
	_, err := ContentDigest(nil)
	assertRegistryCode(t, err, e.CodeReleaseIncomplete)
}

func TestDiskOutputBindingIsExactAndExcludedFromReleaseContentIdentity(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	buildDigest := "sha256:" + strings.Repeat("b", 64)
	planDigest := "sha256:" + strings.Repeat("c", 64)
	valid := Output{
		Name: "node-image", Kind: "disk-image", Digest: digest, BuildDigest: buildDigest,
		Components: []string{"agent", "image"},
		Disk: &DiskOutput{
			PayloadDigest: digest,
			Producer:      "build-image", Component: "image", Format: "qcow2",
			Encryption: "none", Size: 4096, PlanDigest: planDigest, Outcome: "succeeded",
		},
	}
	plainIdentity, err := ContentDigest([]Output{valid})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Output){
		func(output *Output) { output.Disk.Producer = "other-producer" },
		func(output *Output) { output.Disk.Encryption = "luks2" },
		func(output *Output) { output.Disk.Size++ },
		func(output *Output) { output.Disk.PlanDigest = "sha256:" + strings.Repeat("d", 64) },
		func(output *Output) { output.Disk.PayloadDigest = "sha256:" + strings.Repeat("f", 64) },
	} {
		candidate := valid
		candidate.Components = append([]string(nil), valid.Components...)
		candidate.Disk = cloneDiskOutput(valid.Disk)
		change(&candidate)
		identity, err := ContentDigest([]Output{candidate})
		if err != nil {
			t.Fatal(err)
		}
		if identity != plainIdentity {
			t.Fatalf("disk execution metadata changed release-content identity: got %s, want %s", identity, plainIdentity)
		}
	}
	failed := valid
	failed.Disk = cloneDiskOutput(valid.Disk)
	failed.Disk.Outcome = "failed"
	if got, want := projectReleaseContentOutput(failed), projectReleaseContentOutput(valid); got.Name != want.Name ||
		got.Kind != want.Kind || got.Digest != want.Digest || got.BuildDigest != want.BuildDigest ||
		strings.Join(got.Components, "\x00") != strings.Join(want.Components, "\x00") {
		t.Fatal("disk outcome leaked into the frozen release-content projection")
	}

	changedBytes := valid
	changedBytes.Digest = "sha256:" + strings.Repeat("e", 64)
	changedIdentity, err := ContentDigest([]Output{changedBytes})
	if err != nil {
		t.Fatal(err)
	}
	if changedIdentity == plainIdentity {
		t.Fatal("changed disk bytes retained release-content identity")
	}
	cloned := cloneOutputs([]Output{valid})
	cloned[0].Components[0] = "changed"
	cloned[0].Disk.Size++
	if valid.Components[0] != "agent" || valid.Disk.Size != 4096 {
		t.Fatal("disk output cloning retained caller-mutable state")
	}

	for _, tc := range []struct {
		name   string
		change func(*Output)
	}{
		{name: "missing disk binding", change: func(output *Output) { output.Disk = nil }},
		{name: "ordinary output with disk binding", change: func(output *Output) { output.Kind = "executable" }},
		{name: "wrong producer", change: func(output *Output) { output.Disk.Producer = "../build" }},
		{name: "wrong component", change: func(output *Output) { output.Disk.Component = "missing" }},
		{name: "wrong format", change: func(output *Output) { output.Disk.Format = "raw" }},
		{name: "wrong encryption", change: func(output *Output) { output.Disk.Encryption = "aes" }},
		{name: "missing size", change: func(output *Output) { output.Disk.Size = 0 }},
		{name: "wrong plan", change: func(output *Output) { output.Disk.PlanDigest = "mutable" }},
		{name: "missing payload pin", change: func(output *Output) { output.Disk.PayloadDigest = "" }},
		{name: "malformed payload pin", change: func(output *Output) { output.Disk.PayloadDigest = "mutable" }},
		{name: "wrong outcome", change: func(output *Output) { output.Disk.Outcome = "failed" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := valid
			candidate.Components = append([]string(nil), valid.Components...)
			candidate.Disk = cloneDiskOutput(valid.Disk)
			tc.change(&candidate)
			_, err := ContentDigest([]Output{candidate})
			assertRegistryCode(t, err, e.CodeVerifyFailed)
		})
	}
}
