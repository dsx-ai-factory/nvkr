// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package pipeline

import (
	"slices"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestRunPackerContainerPublicBootstrapAdmission(t *testing.T) {
	for _, test := range []struct {
		name      string
		variables []ir.PackerVariable
		allow     bool
	}{
		{name: "public bootstrap", variables: []ir.PackerVariable{{Name: "luks_passphrase", Value: "changeit"}}, allow: true},
		{name: "implicit default"},
		{name: "private value", variables: []ir.PackerVariable{{Name: "luks_passphrase", Value: "private-value-must-not-appear"}}},
		{name: "duplicate", variables: []ir.PackerVariable{
			{Name: "luks_passphrase", Value: "changeit"}, {Name: "luks_passphrase", Value: "changeit"},
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, runtime, buildCreated := newPackerContainerTestRun(t, "Packer v1.14.2\n",
				"The configuration is valid.\n", "", nil)
			request.node.Disk.Encryption = "luks2"
			request.node.Packer.Variables = test.variables
			artifact, err := runPackerContainer(testContext(t), request)
			if !test.allow {
				if artifact != nil || code(err) != e.CodeEngineCapability || len(runtime.calls) != 0 || len(runtime.cleanups) != 0 || *buildCreated {
					t.Fatalf("rejected FDE caused runtime work: artifact=%v error=%v calls=%v cleanup=%v", artifact, err, runtime.calls, runtime.cleanups)
				}
				if strings.Contains(err.Error(), "private-value-must-not-appear") {
					t.Fatal("rejected FDE exposed a private value")
				}
				return
			}
			if err != nil || artifact == nil || !*buildCreated {
				t.Fatalf("public-bootstrap Packer artifact=%v error=%v created=%t", artifact, err, *buildCreated)
			}
			if err := artifact.Close(); err != nil {
				t.Fatal(err)
			}
			bound := 0
			for _, call := range runtime.calls {
				if len(call) > 0 && call[0] == "create" && (slices.Contains(call, "validate") || slices.Contains(call, "build")) {
					if !slices.Contains(call, "luks_passphrase=changeit") {
						t.Fatalf("public bootstrap missing from Packer invocation: %v", call)
					}
					bound++
				}
			}
			if bound != 2 {
				t.Fatalf("public bootstrap bound to %d invocations, want validate and build", bound)
			}
		})
	}
}
