// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestGenerateInventoryAdmissionBeforeCompilation(t *testing.T) {
	// All selected files are deliberately absent: malformed inventory must fail
	// admission without resolving any input or creating an output directory.
	for _, raw := range []string{
		`[]`,
		`[{"bundle":"nvkr-run","group":"main","definition":"missing.yaml","environment":"missing.yaml","ignored":true}]`,
		`[{"bundle":"nvkr-run","group":"main","group":"nightly","definition":"missing.yaml","environment":"missing.yaml"}]`,
		`[{"bundle":"nvkr-run","group":"unknown","definition":"missing.yaml","environment":"missing.yaml"}]`,
		`[{"bundle":"nvkr-run","group":"main","definition":"../missing.yaml","environment":"missing.yaml"}]`,
		`[{"bundle":"nvkr-run","group":"main","definition":"missing.yaml","environment":"missing.yaml","runtime":"false"}]`,
	} {
		t.Run(raw, func(t *testing.T) {
			root := t.TempDir()
			input := filepath.Join(root, "inventory.json")
			if err := os.WriteFile(input, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			err := GenerateInventory(t.Context(), input, "", "test", root)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != e.CodePipelineDrift {
				t.Fatalf("expected inventory admission failure, got %v", err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 {
				t.Fatal("admission changed output root", err)
			}
		})
	}
}
