// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestMirrorInventoryV1GoldenBytes(t *testing.T) {
	for _, form := range []string{"minimal", "full"} {
		t.Run(form, func(t *testing.T) {
			// Captured with the original v1 structs, before the inventory gained
			// its own explicitly tagged object and subject wire types.
			raw, err := os.ReadFile(filepath.Join("testdata", "publication-mirror-v1-"+form+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var inventory mirrorInventory
			if err := canonical.UnmarshalExact(raw, &inventory); err != nil {
				t.Fatal(err)
			}
			// Exercise both API/wire conversions with every populated field.
			for i, object := range inventory.Objects {
				inventory.Objects[i] = mirrorObject(object.storedObject())
			}
			for i, proof := range inventory.Proofs {
				subject := proof.Subject.signingSubject()
				inventory.Proofs[i].Subject = mirrorSubject{Ref: subject.Ref, Digest: subject.Digest}
			}
			encoded, err := canonical.Marshal(inventory)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(encoded, raw) {
				t.Fatalf("v1 inventory bytes changed:\ngot  %s\nwant %s", encoded, raw)
			}
			locator := mirrorWireLocator(raw, inventory.RootReference)
			if _, err := validateMirrorInventory(inventory, locator, mirrorFixtureLimits()); err != nil {
				t.Fatalf("golden must describe a valid inventory: %v", err)
			}
		})
	}
}

func mirrorWireLocator(raw []byte, reference string) StoredObject {
	digest := digestBytes(raw)
	root := strings.SplitN(reference, "@", 2)[1]
	return StoredObject{Object: Object{Key: "publications/" + strings.TrimPrefix(root, "sha256:") + "/" + strings.TrimPrefix(digest, "sha256:") + ".json", SHA256: digest, Size: int64(len(raw)), MediaType: "application/json"}, VersionID: "inventory-version"}
}

func TestRestoreMirrorStrictV1WireDispatchBeforeBlobIO(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "publication-mirror-v1-minimal.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"missing version", "unknown version", "unknown inventory field", "unknown object field", "unknown subject field", "renamed object key", "renamed subject key", "zero inventory"} {
		t.Run(scenario, func(t *testing.T) {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			reference := ""
			if err := json.Unmarshal(fields["rootReference"], &reference); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "missing version":
				delete(fields, "schemaVersion")
				fields["objects"] = json.RawMessage(`false`) // Version dispatch precedes v1 semantic decoding.
			case "unknown version":
				fields["schemaVersion"] = json.RawMessage(`"nvkr.dev/publication-mirror/v2"`)
				fields["objects"] = json.RawMessage(`false`)
			case "unknown inventory field":
				fields["unknown"] = json.RawMessage(`true`)
			case "unknown object field":
				fields["objects"] = bytes.Replace(fields["objects"], []byte(`{"ETag":`), []byte(`{"Additional":true,"ETag":`), 1)
			case "unknown subject field":
				fields["proofs"] = bytes.Replace(fields["proofs"], []byte(`"subject":{"Digest":`), []byte(`"subject":{"Additional":true,"Digest":`), 1)
			case "renamed object key":
				fields["objects"] = bytes.Replace(fields["objects"], []byte(`"Key":`), []byte(`"key":`), 1)
			case "renamed subject key":
				fields["proofs"] = bytes.Replace(fields["proofs"], []byte(`"Ref":`), []byte(`"ref":`), 1)
			case "zero inventory":
				fields = map[string]json.RawMessage{}
			}
			changed, err := canonical.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			locator := mirrorWireLocator(changed, reference)
			store := &mirrorMemoryStore{data: map[string][]byte{locator.Key: changed}}
			out := filepath.Join(t.TempDir(), "refused")
			result, err := RestoreMirror(t.Context(), store, locator, out, mirrorFixtureLimits())
			assertRegistryCode(t, err, e.CodeVerifyFailed)
			if (scenario == "missing version" || scenario == "unknown version") && !strings.Contains(err.Error(), "schema version") {
				t.Fatalf("semantic fields were decoded before version dispatch: %v", err)
			}
			if result != nil || store.reads != 1 {
				t.Fatalf("invalid inventory reached blob I/O: result=%v reads=%d", result, store.reads)
			}
			if _, err := os.Lstat(out); !os.IsNotExist(err) {
				t.Fatalf("invalid inventory exposed output: %v", err)
			}
		})
	}
}
