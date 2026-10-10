// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestHostedOutputMembershipUsesOneExactSubjectContract(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*delivery.Output)
	}{
		{"name", func(o *delivery.Output) { o.Name = "sibling" }},
		{"kind", func(o *delivery.Output) { o.Kind = "container-image" }},
		{"manifest", func(o *delivery.Output) { o.Digest = "other" }},
		{"build", func(o *delivery.Output) { o.BuildDigest = "other" }},
		{"components", func(o *delivery.Output) { o.Components = []string{"sibling"} }},
		{"missing disk", func(o *delivery.Output) { o.Disk = nil }},
		{"payload", func(o *delivery.Output) { o.Disk.PayloadDigest = "other" }},
		{"producer", func(o *delivery.Output) { o.Disk.Producer = "sibling" }},
		{"component", func(o *delivery.Output) { o.Disk.Component = "sibling" }},
		{"format", func(o *delivery.Output) { o.Disk.Format = "raw" }},
		{"encryption", func(o *delivery.Output) { o.Disk.Encryption = "luks2" }},
		{"size", func(o *delivery.Output) { o.Disk.Size++ }},
		{"plan", func(o *delivery.Output) { o.Disk.PlanDigest = "other" }},
		{"outcome", func(o *delivery.Output) { o.Disk.Outcome = "failed" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			observed := delivery.Output{Name: "image", Kind: "disk-image", Digest: "manifest", BuildDigest: "build", Components: []string{"image"},
				Disk: &delivery.DiskOutput{PayloadDigest: "payload", Producer: "producer", Component: "image", Format: "qcow2",
					Encryption: "none", Size: 10, PlanDigest: "plan", Outcome: "succeeded"}}
			member := observed
			disk := *observed.Disk
			member.Disk = &disk
			member.Reference = "registry.example.test/relocated"
			if err := requireHostedOutputMembership(observed, member); err != nil {
				t.Fatalf("transport relocation changed membership: %v", err)
			}
			test.edit(&member)
			assertCode(t, requireHostedOutputMembership(observed, member), e.CodeVerifyFailed)
		})
	}
	plain := delivery.Output{Name: "cli", Kind: "binary-bundle", Digest: "manifest", BuildDigest: "build"}
	if err := requireHostedOutputMembership(plain, plain); err != nil {
		t.Fatal(err)
	}
	withDisk := plain
	withDisk.Disk = &delivery.DiskOutput{}
	assertCode(t, requireHostedOutputMembership(plain, withDisk), e.CodeVerifyFailed)
	assertCode(t, requireHostedOutputMembership(withDisk, plain), e.CodeVerifyFailed)
}
