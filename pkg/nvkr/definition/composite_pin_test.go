// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// pinFixture writes a commented YAML composite whose beta pin is stale.
func pinFixture(t *testing.T) (root string, raw []byte, stale, current string) {
	t.Helper()
	root, composite := compositeFixture(t)
	edited := compositeMemberFixture("beta", "beta-cli", "beta-tool")
	writeCompositeFile(t, filepath.Join(root, "defs", "beta.yaml"), edited)
	current, err := edited.Digest()
	if err != nil {
		t.Fatal(err)
	}
	stale = composite.Members[1].Digest
	raw = []byte("# Product composite.\napiVersion: nvkr.dev/composite-definition/v1\nkind: CompositeDefinition\nmetadata:\n  name: product\nmembers:\n" +
		"  - name: alpha   # first family\n    definition: alpha.yaml\n    digest: " + composite.Members[0].Digest + "\n" +
		"  - name: beta\n    definition: beta.yaml\n    digest: " + stale + "\n")
	writeCompositeRaw(t, filepath.Join(root, "defs", "composite.yaml"), raw)
	return root, raw, stale, current
}

func TestCompositePinContract(t *testing.T) {
	t.Run("check reports drift without writing", func(t *testing.T) {
		root, raw, stale, current := pinFixture(t)
		report, err := PinComposite(t.Context(), root, "defs/composite.yaml", true)
		requireCode(t, err, e.CodeUnverifiedRef)
		if report.Written || len(report.Drift) != 1 || report.Drift[0] != (MemberPin{Name: "beta", Pinned: stale, Current: current}) {
			t.Fatalf("check report = %+v", report)
		}
		after, _ := os.ReadFile(filepath.Join(root, "defs", "composite.yaml"))
		if !bytes.Equal(after, raw) {
			t.Fatal("check wrote the composite")
		}
		_, err = LoadComposite(t.Context(), root, "defs/composite.yaml")
		requireCode(t, err, e.CodeUnverifiedRef)
	})
	t.Run("rewrite replaces only the stale pin", func(t *testing.T) {
		root, raw, stale, current := pinFixture(t)
		report, err := PinComposite(t.Context(), root, "defs/composite.yaml", false)
		if err != nil || !report.Written {
			t.Fatalf("pin = %+v, %v", report, err)
		}
		after, _ := os.ReadFile(filepath.Join(root, "defs", "composite.yaml"))
		if want := strings.Replace(string(raw), stale, current, 1); string(after) != want {
			t.Fatalf("pin changed more than the stale digest:\n%s", after)
		}
		if _, err := LoadComposite(t.Context(), root, "defs/composite.yaml"); err != nil {
			t.Fatal(err)
		}
		report, err = PinComposite(t.Context(), root, "defs/composite.yaml", true)
		if err != nil || report.Written || len(report.Drift) != 0 {
			t.Fatalf("re-check after pin = %+v, %v", report, err)
		}
	})
	t.Run("unadmissible result is not written", func(t *testing.T) {
		root, raw, _, _ := pinFixture(t)
		writeCompositeFile(t, filepath.Join(root, "defs", "beta.yaml"), compositeMemberFixture("beta", "alpha-cli"))
		_, err := PinComposite(t.Context(), root, "defs/composite.yaml", false)
		requireCode(t, err, e.CodeInvalidCatalog)
		after, _ := os.ReadFile(filepath.Join(root, "defs", "composite.yaml"))
		if !bytes.Equal(after, raw) {
			t.Fatal("pin wrote a composite that does not admit")
		}
	})
	t.Run("concurrent writer wins", func(t *testing.T) {
		root, raw, stale, current := pinFixture(t)
		concurrent := append([]byte("# concurrent edit\n"), raw...)
		writeCompositeRaw(t, filepath.Join(root, "defs", "composite.yaml"), concurrent)
		err := replaceFileCAS(t.Context(), root, "defs/composite.yaml", raw, []byte(strings.Replace(string(raw), stale, current, 1)))
		requireCode(t, err, e.CodeWriteConflict)
		after, _ := os.ReadFile(filepath.Join(root, "defs", "composite.yaml"))
		if !bytes.Equal(after, concurrent) {
			t.Fatal("stale pin writer overwrote a concurrent edit")
		}
	})
	t.Run("active writer guard", func(t *testing.T) {
		root, raw, _, _ := pinFixture(t)
		guard, err := os.OpenFile(filepath.Join(root, "defs", ".composite.yaml.pin.lock"), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer guard.Close()
		if err := syscall.Flock(int(guard.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			t.Fatal(err)
		}
		_, err = PinComposite(t.Context(), root, "defs/composite.yaml", false)
		requireCode(t, err, e.CodeWriteConflict)
		after, _ := os.ReadFile(filepath.Join(root, "defs", "composite.yaml"))
		if !bytes.Equal(after, raw) {
			t.Fatal("guarded pin wrote the composite")
		}
	})
}

func TestLoadDefinitionRefusesComposite(t *testing.T) {
	root, composite := compositeFixture(t)
	writeCompositeFile(t, filepath.Join(root, "defs", "composite.yaml"), composite)
	_, err := LoadDefinition(t.Context(), filepath.Join(root, "defs", "composite.yaml"))
	requireCode(t, err, e.CodeEngineCapability)
	t.Chdir(root)
	input, err := LoadInput(t.Context(), "defs/composite.yaml")
	if err != nil || input.Kind != CompositeKind || input.Composition == nil || input.Digest != input.Composition.Digest() {
		t.Fatalf("composite input = %+v, %v", input, err)
	}
	plain, err := LoadInput(t.Context(), "defs/alpha.yaml")
	if err != nil || plain.Kind != DefinitionKind || plain.Composition != nil || plain.Digest != composite.Members[0].Digest {
		t.Fatalf("plain input = %+v, %v", plain, err)
	}
	outside := filepath.Join(t.TempDir(), "outside.yaml")
	writeCompositeFile(t, outside, composite)
	_, err = LoadInput(t.Context(), outside)
	requireCode(t, err, e.CodeInvalidCatalog)
	if !strings.Contains(err.Error(), "beneath the working directory") {
		t.Fatalf("outside composite refused for another reason: %v", err)
	}
}
