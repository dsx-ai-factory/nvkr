// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComparison(t *testing.T) {
	for _, tc := range []struct {
		name string
		exit int
	}{
		{"equal", 0}, {"order", 1}, {"content", 1}, {"identity", 1}, {"added", 1}, {"removed", 1},
		{"unbound", 2}, {"duplicate", 2}, {"empty", 2}, {"missing identity", 2}, {"trailing", 2}, {"report limit", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var directories []string
			for i := 0; i < 2; i++ {
				directory := t.TempDir()
				directories = append(directories, directory)
				production := record{}
				for _, field := range identityFields {
					production[field] = "same"
				}
				entries := []record{{"path": "a", "sha256": "same"}, {"path": "b", "sha256": "same"}}
				layer := "same"
				if i == 1 {
					switch tc.name {
					case "order":
						entries[0], entries[1] = entries[1], entries[0]
					case "content":
						entries[0]["sha256"] = "changed"
					case "identity":
						production["buildDigest"] = "changed"
					case "unbound":
						layer = "changed"
					case "duplicate":
						entries = append(entries, entries[0])
					case "empty":
						entries = nil
					case "missing identity":
						delete(production, "size")
					case "added":
						entries = append(entries, record{"path": "c"})
					case "removed":
						entries = entries[:1]
					case "report limit":
						for j := 0; j < 101; j++ {
							entries = append(entries, record{"path": fmt.Sprintf("extra-%03d", j)})
						}
					}
				}
				for name, value := range map[string]any{"disk-result.json": record{"runtimeUse": record{"production": production}}, "runtime-tar-entries.json": record{"layerDigest": layer, "entries": entries}} {
					raw, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					if i == 1 && tc.name == "trailing" {
						raw = append(raw, []byte(" {}")...)
					}
					if err := os.WriteFile(filepath.Join(directory, name), raw, 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			var stdout, stderr bytes.Buffer
			if got := run(t.Context(), directories, &stdout, &stderr); got != tc.exit {
				t.Fatalf("exit=%d want=%d: %s", got, tc.exit, stderr.String())
			}
			if tc.exit == 2 {
				if !strings.Contains(stderr.String(), "NVKR-E-VERIFY-FAILED") {
					t.Fatal(stderr.String())
				}
				return
			}
			var got report
			if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Equal != (tc.exit == 0) {
				t.Fatalf("wrong equality: %+v", got)
			}
			if tc.name == "content" && (got.Entries[0].Path != "a" || got.Entries[0].Fields["sha256"][1] != "changed") {
				t.Fatal(got)
			}
			if tc.name == "report limit" && (got.ChangedEntryCount != 101 || len(got.Entries) != 100) {
				t.Fatal(got)
			}
		})
	}
}
