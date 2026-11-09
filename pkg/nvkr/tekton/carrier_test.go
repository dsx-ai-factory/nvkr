// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package tekton

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func carrierFixture(t *testing.T) (string, *TaskReceipt, CarrierManifest, map[string]string) {
	t.Helper()
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "payload"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "payload", "tool"), []byte("exact output"), 0755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(source, CarrierFile(0))
	if err := transportarchive.Pack(t.Context(), archive, source, []string{"payload"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	plan := "sha256:" + strings.Repeat("a", 64)
	manifest := CarrierManifest{SchemaVersion: CarrierSchema, PlanDigest: plan, Carriers: []Carrier{{ID: "output/tool", Path: "payload", Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), Size: int64(len(raw))}}}
	return source, &TaskReceipt{taskUID: "synthetic", planDigest: plan, state: "success", results: map[string]string{}}, manifest, map[string]string{"output/tool": "payload"}
}

func bindCarrierFixture(t *testing.T, receipt *TaskReceipt, manifest CarrierManifest) []byte {
	t.Helper()
	raw, err := canonical.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	receipt.results["carrier"] = fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	return raw
}

func TestCarrierRestoreRejectsChangedOrMissingBytes(t *testing.T) {
	for _, mode := range []string{"exact", "corruption", "omission", "extra bytes"} {
		t.Run(mode, func(t *testing.T) {
			source, receipt, manifest, expected := carrierFixture(t)
			raw := bindCarrierFixture(t, receipt, manifest)
			set, err := AuthenticateCarriers(t.Context(), receipt, raw, expected)
			if err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(source, CarrierFile(0))
			switch mode {
			case "corruption":
				file, err := os.OpenFile(archive, os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				_, err = file.WriteAt([]byte("wrong"), 512)
				if err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			case "omission":
				if err := os.Remove(archive); err != nil {
					t.Fatal(err)
				}
			case "extra bytes":
				file, err := os.OpenFile(archive, os.O_WRONLY|os.O_APPEND, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.Write([]byte("extra")); err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			}
			destination := t.TempDir()
			err = set.Restore(t.Context(), source, destination, []string{"output/tool"})
			if mode == "exact" {
				if err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(filepath.Join(destination, "payload", "tool"))
				if err != nil || string(got) != "exact output" {
					t.Fatalf("restored bytes: %q %v", got, err)
				}
			} else {
				if err == nil {
					t.Fatal("invalid carrier restored")
				}
				if _, err := os.Stat(filepath.Join(destination, "payload")); !os.IsNotExist(err) {
					t.Fatal("invalid carrier published output")
				}
			}
		})
	}
}

func TestCarrierMembershipCannotAuthorizeSiblingOutputs(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*CarrierManifest)
	}{
		{"wrong Plan", func(m *CarrierManifest) { m.PlanDigest = "sha256:" + strings.Repeat("b", 64) }},
		{"sibling output", func(m *CarrierManifest) { m.Carriers[0].ID = "output/sibling" }},
		{"foreign path", func(m *CarrierManifest) { m.Carriers[0].Path = "other" }},
		{"missing output", func(m *CarrierManifest) { m.Carriers = nil }},
		{"duplicate output", func(m *CarrierManifest) { m.Carriers = append(m.Carriers, m.Carriers[0]) }},
		{"missing digest", func(m *CarrierManifest) { m.Carriers[0].Digest = "" }},
		{"excess size", func(m *CarrierManifest) { m.Carriers[0].Size = transportarchive.MaxArchiveBytes + 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, receipt, manifest, expected := carrierFixture(t)
			tc.edit(&manifest)
			raw := bindCarrierFixture(t, receipt, manifest)
			set, err := AuthenticateCarriers(t.Context(), receipt, raw, expected)
			requireCode(t, err, e.CodeVerifyFailed)
			if set != nil {
				t.Fatal("invalid manifest issued receipt")
			}
		})
	}
}
