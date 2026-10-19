// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"bytes"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestStageSubjectBinding(t *testing.T) {
	input := "sha256:" + strings.Repeat("a", 64)
	subject := "sha256:" + strings.Repeat("b", 64)
	bom, err := NewSubject("sha256:"+strings.Repeat("c", 64), "component", input)
	if err != nil {
		t.Fatal(err)
	}
	original, err := bom.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"build-output", "source-build", "packaging"} {
		t.Run(stage, func(t *testing.T) {
			bound, err := ForSubject(bom, subject, stage, input)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := bound.Marshal()
			if err != nil {
				t.Fatal(err)
			}
			again, err := ForSubject(bound, subject, stage, input)
			if err != nil {
				t.Fatal(err)
			}
			repeated, err := again.Marshal()
			if err != nil || !bytes.Equal(raw, repeated) {
				t.Fatal("stage binding is not idempotent", err)
			}
			unchanged, err := bom.Marshal()
			if err != nil || !bytes.Equal(original, unchanged) {
				t.Fatal("binding mutated source inventory", err)
			}
			bound.Metadata.Component.Hashes[0].Content = strings.Repeat("d", 64)
			if err := bound.Validate(); err == nil {
				t.Fatal("subject substitution retained valid serial identity")
			}
		})
	}
	for _, tc := range []struct{ subject, stage, input string }{{"invalid", "packaging", input}, {subject, "unknown", input}, {subject, "packaging", "invalid"}} {
		_, err := ForSubject(bom, tc.subject, tc.stage, tc.input)
		if err == nil || e.ExitCode(err) != e.ExitCode(e.New(e.CodeUnverifiedRef, "")) {
			t.Fatalf("invalid binding: %v", err)
		}
	}
}
