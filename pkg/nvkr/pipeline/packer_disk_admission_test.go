// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

func TestValidateVMImagePackerDiskExecution(t *testing.T) {
	const privateValue = "private-value-must-not-appear"
	for _, test := range []struct {
		name   string
		change func(*ir.Node)
		allow  bool
	}{
		{name: "public bootstrap", allow: true},
		{name: "unencrypted", change: func(node *ir.Node) { node.Disk.Encryption = "none" }, allow: true},
		{name: "missing disk", change: func(node *ir.Node) { node.Disk = nil }},
		{name: "unsupported format", change: func(node *ir.Node) { node.Disk.Format = "raw" }},
		{name: "unknown encryption", change: func(node *ir.Node) { node.Disk.Encryption = "unknown" }},
		{name: "missing Packer", change: func(node *ir.Node) { node.Packer = nil }},
		{name: "command", change: func(node *ir.Node) { node.Command = []string{"make", "fde"} }},
		{name: "other builder", change: func(node *ir.Node) { node.BuilderType = ir.BuilderOCIHermetic }},
		{name: "implicit default", change: func(node *ir.Node) { node.Packer.Variables = nil }},
		{name: "empty value", change: func(node *ir.Node) { node.Packer.Variables[0].Value = "" }},
		{name: "private value", change: func(node *ir.Node) { node.Packer.Variables[0].Value = privateValue }},
		{name: "duplicate public value", change: func(node *ir.Node) {
			node.Packer.Variables = append(node.Packer.Variables, node.Packer.Variables[0])
		}},
		{name: "duplicate private value", change: func(node *ir.Node) {
			node.Packer.Variables = append(node.Packer.Variables,
				ir.PackerVariable{Name: "luks_passphrase", Value: privateValue})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			node := ir.Node{BuilderType: ir.BuilderVMImage,
				Disk:   &ir.DiskSpec{Format: "qcow2", Encryption: "luks2"},
				Packer: &ir.PackerConfig{Variables: []ir.PackerVariable{{Name: "luks_passphrase", Value: "changeit"}}},
			}
			if test.change != nil {
				test.change(&node)
			}
			err := ValidateVMImagePackerDiskExecution(node)
			if test.allow {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if code(err) != e.CodeEngineCapability {
				t.Fatalf("disk admission error = %v, want %s", err, e.CodeEngineCapability)
			}
			if strings.Contains(err.Error(), privateValue) {
				t.Fatal("disk admission exposed a rejected value")
			}
		})
	}
}
