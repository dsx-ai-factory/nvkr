// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package status

import (
	"errors"
	"strings"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const target = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const replacement = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func code(err error) nvkrerr.Code {
	var coded *nvkrerr.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

func declaration(action string) StatusDeclaration {
	return StatusDeclaration{SchemaVersion: SchemaVersion, Target: target, Action: action}
}

func TestDeclarationValidationIncludesSchemaActionAndTime(t *testing.T) {
	tests := []struct {
		name string
		decl StatusDeclaration
		code nvkrerr.Code
	}{
		{name: "schema", decl: StatusDeclaration{Target: target, Action: ActionWithdrawn}, code: nvkrerr.CodeInvalidCatalog},
		{name: "target", decl: StatusDeclaration{SchemaVersion: SchemaVersion, Target: "not-digest", Action: ActionWithdrawn}, code: nvkrerr.CodeUnverifiedRef},
		{name: "action", decl: StatusDeclaration{SchemaVersion: SchemaVersion, Target: target, Action: "bogus"}, code: nvkrerr.CodeInvalidCatalog},
		{name: "superseded target", decl: StatusDeclaration{SchemaVersion: SchemaVersion, Target: target, Action: ActionSuperseded}, code: nvkrerr.CodeInvalidCatalog},
		{name: "negative time", decl: StatusDeclaration{SchemaVersion: SchemaVersion, Target: target, Action: ActionWithdrawn, EffectiveEpoch: -1}, code: nvkrerr.CodeInvalidCatalog},
		{name: "extraneous replacement", decl: StatusDeclaration{SchemaVersion: SchemaVersion, Target: target, Action: ActionWithdrawn, ReplacedBy: replacement}, code: nvkrerr.CodeInvalidCatalog},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := code(test.decl.Validate()); got != test.code {
				t.Fatalf("got %s, want %s", got, test.code)
			}
		})
	}
	valid := declaration(ActionSuperseded)
	valid.ReplacedBy = replacement
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid declaration rejected: %v", err)
	}
}

func TestExplicitAsOfAndReason(t *testing.T) {
	if _, err := At(0); code(err) != nvkrerr.CodeInvalidCatalog {
		t.Fatalf("zero explicit as-of must be rejected: %v", err)
	}
	if !strings.Contains(reason("eol"), "eol") {
		t.Fatal("reason rendering lost text")
	}
}
