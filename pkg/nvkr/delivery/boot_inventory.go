// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"reflect"
	"slices"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const bootInventorySchema = "nvkr.dev/publication-boot-selection/v1"

// These expectations come from the consumer, never from discovery. Syntax and
// byte integrity do not authenticate the named original subjects or policy.
type BootInventoryExpectation struct {
	RootReference     string                            `json:"rootReference"`
	Invocation        string                            `json:"invocation"`
	DefinitionDigest  string                            `json:"definitionDigest"`
	EnvironmentDigest string                            `json:"environmentDigest"`
	PlanDigest        string                            `json:"planDigest"`
	Selection         definition.BootSelection          `json:"selection"`
	Destination       definition.ObjectStoreDestination `json:"destination"`
}

type bootInventoryUse struct {
	Binding            BootAssetBinding `json:"binding"`
	PreparedInvocation string           `json:"preparedInvocation"`
	SignedInvocation   string           `json:"signedInvocation"`
	URL                string           `json:"url"`
}

// BootOriginalUse describes an original selected binding and its transport
// mapping. Only lifecycle can authenticate its signatures and membership.
type BootOriginalUse = bootInventoryUse

type bootMetadataRecord struct {
	Reference  string         `json:"reference"`
	RootDigest string         `json:"rootDigest"`
	Outputs    []string       `json:"outputs"`
	Members    []mirrorMember `json:"members"`
}

type bootStoredObject struct {
	Key       string   `json:"key"`
	SHA256    string   `json:"sha256"`
	MediaType string   `json:"mediaType"`
	Size      int64    `json:"size"`
	VersionID string   `json:"versionId,omitempty"`
	ETag      string   `json:"etag,omitempty"`
	Roles     []string `json:"roles"`
}

func (record bootStoredObject) stored() StoredObject {
	return StoredObject{Object: Object{Key: record.Key, SHA256: record.SHA256, MediaType: record.MediaType, Size: record.Size}, VersionID: record.VersionID, ETag: record.ETag}
}

// Every stored object's logical uses are the complete set of references to its
// digest in Uses, Publications.Members and Proofs. Roles must equal that derived
// set, so shared bytes cannot smuggle an unbound object into the inventory.
type bootInventory struct {
	SchemaVersion string                   `json:"schemaVersion"`
	Expected      BootInventoryExpectation `json:"expected"`
	Uses          []bootInventoryUse       `json:"uses"`
	Publications  []bootMetadataRecord     `json:"publications"`
	Proofs        []mirroredProof          `json:"proofs"`
	Objects       []bootStoredObject       `json:"objects"`
}

func (expected BootInventoryExpectation) Validate() error {
	if err := ValidatePublicationReference(expected.RootReference); err != nil {
		return err
	}
	if expected.Invocation == "" || len(expected.Invocation) > 2048 ||
		!canonical.IsDigest(expected.DefinitionDigest) || !canonical.IsDigest(expected.EnvironmentDigest) || !canonical.IsDigest(expected.PlanDigest) {
		return e.New(e.CodeVerifyFailed, "boot inventory requires independently expected exact inputs and invocation")
	}
	if err := expected.Selection.Validate(); err != nil {
		return err
	}
	if err := expected.Destination.Validate(); err != nil {
		return err
	}
	if expected.Destination.Mode != definition.ObjectStoreBootMode {
		return e.New(e.CodeEngineCapability, "boot inventory requires a selective destination")
	}
	return nil
}

func bootInventoryKey(reference, rawDigest string) string {
	_, root, _ := strings.Cut(reference, "@")
	return "boot-publications/" + strings.TrimPrefix(root, "sha256:") + "/" + strings.TrimPrefix(rawDigest, "sha256:") + ".json"
}

// Decode before any object access. Canonical decoding rejects duplicate keys,
// unknown fields and alternative encodings, including the full-mirror schema.
func decodeBootInventory(raw []byte, locator StoredObject, expected BootInventoryExpectation, limits BootTransferLimits) (bootInventory, error) {
	if err := expected.Validate(); err != nil {
		return bootInventory{}, err
	}
	if len(raw) == 0 || int64(len(raw)) > maxMirrorInventoryBytes || int64(len(raw)) != locator.Size || digestBytes(raw) != locator.SHA256 {
		return bootInventory{}, e.New(e.CodeVerifyFailed, "boot inventory differs from its pinned exact bytes or control limit")
	}
	var inventory bootInventory
	if err := canonical.UnmarshalExact(raw, &inventory); err != nil {
		return bootInventory{}, e.Wrap(e.CodeVerifyFailed, err, "decoding canonical selected boot inventory")
	}
	if err := validateBootInventory(inventory, locator, expected, limits); err != nil {
		return bootInventory{}, err
	}
	return inventory, nil
}

func validateBootInventory(inventory bootInventory, locator StoredObject, expected BootInventoryExpectation, limits BootTransferLimits) error {
	if err := expected.Validate(); err != nil {
		return err
	}
	if inventory.SchemaVersion != bootInventorySchema || !reflect.DeepEqual(inventory.Expected, expected) ||
		locator.Key != bootInventoryKey(expected.RootReference, locator.SHA256) || locator.MediaType != "application/json" ||
		!canonical.IsDigest(locator.SHA256) || locator.Size <= 0 || locator.Size > maxMirrorInventoryBytes {
		return e.New(e.CodeVerifyFailed, "boot inventory differs from externally supplied subject, inputs, selection, destination or mapping")
	}
	if err := validateBootPin(locator); err != nil {
		return err
	}
	bindings := make([]BootAssetBinding, len(inventory.Uses))
	for index, use := range inventory.Uses {
		bindings[index] = use.Binding
	}
	if err := admitBootStorage(expected.Selection, bindings, limits); err != nil {
		return err
	}
	if len(inventory.Objects)+1 > limits.Storage.MaxObjects || locator.Size > limits.Storage.MaxObjectBytes || locator.Size > limits.Storage.MaxTotalBytes {
		return e.New(e.CodeEngineCapability, "selected inventory exceeds admitted storage bounds")
	}
	objects := make(map[string]StoredObject, len(inventory.Objects))
	roles := make(map[string]map[string]bool, len(inventory.Objects))
	total := locator.Size
	previous := ""
	for _, record := range inventory.Objects {
		object := record.stored()
		if !canonical.IsDigest(object.SHA256) || object.SHA256 <= previous || object.Key != mirrorBlobKey(object.SHA256) || object.MediaType != MediaOctetStream || object.Size < 0 {
			return e.New(e.CodeVerifyFailed, "boot inventory has an invalid, repeated or unordered object")
		}
		if err := validateBootPin(object); err != nil {
			return err
		}
		if object.Size > limits.Storage.MaxObjectBytes || object.Size > limits.Storage.MaxTotalBytes-total {
			return e.New(e.CodeEngineCapability, "selected inventory exceeds complete unique-byte limits")
		}
		total += object.Size
		objects[object.SHA256], roles[object.SHA256] = object, map[string]bool{}
		previous = object.SHA256
	}
	useObject := func(digest, role string) (StoredObject, error) {
		object, found := objects[digest]
		if !found {
			return StoredObject{}, e.New(e.CodeVerifyFailed, "boot inventory omits an object needed by a logical use")
		}
		roles[digest][role] = true
		return object, nil
	}
	names := make([]string, 0, len(inventory.Uses))
	locals := make(map[string]string, len(inventory.Uses))
	previous = ""
	for _, use := range inventory.Uses {
		if use.Binding.Output <= previous || use.PreparedInvocation == "" || use.SignedInvocation == "" || len(use.PreparedInvocation) > 2048 || len(use.SignedInvocation) > 2048 {
			return e.New(e.CodeVerifyFailed, "boot inventory has invalid named uses or original invocations")
		}
		object, err := useObject(use.Binding.SHA256, "payload")
		if err != nil {
			return err
		}
		if object.Object != bootPayloadObject(use.Binding) {
			return e.New(e.CodeVerifyFailed, "boot stored object differs from selected raw binding")
		}
		location, err := bootServingURL(*expected.Destination.Serving, object.Object)
		if err != nil || location != use.URL {
			return e.New(e.CodeVerifyFailed, "boot inventory URL differs from externally authorized mapping")
		}
		if _, exists := locals[use.Binding.Publication]; exists || use.Binding.Publication == expected.RootReference {
			return e.New(e.CodeVerifyFailed, "boot use does not name a distinct output-local original root")
		}
		locals[use.Binding.Publication] = use.Binding.Output
		names = append(names, use.Binding.Output)
		previous = use.Binding.Output
	}
	if len(inventory.Publications) != len(locals)+1 {
		return e.New(e.CodeVerifyFailed, "boot inventory omits or adds original proof roots")
	}
	controlDigests := map[string]bool{}
	rootFound := false
	previous = ""
	for _, publication := range inventory.Publications {
		if err := ValidatePublicationReference(publication.Reference); err != nil {
			return err
		}
		_, digest, _ := strings.Cut(publication.Reference, "@")
		if publication.Reference <= previous || digest != publication.RootDigest || len(publication.Members) == 0 || len(publication.Members) > maxDeliveryObjects*4 {
			return e.New(e.CodeVerifyFailed, "boot original root has invalid sorted identity or control membership")
		}
		previous = publication.Reference
		wanted := []string{locals[publication.Reference]}
		if publication.Reference == expected.RootReference {
			wanted, rootFound = names, true
		} else if wanted[0] == "" {
			return e.New(e.CodeVerifyFailed, "boot inventory contains a foreign original root")
		}
		if !slices.Equal(publication.Outputs, wanted) {
			return e.New(e.CodeVerifyFailed, "boot root control selection differs from exact named uses")
		}
		seenIndex := false
		var rootBytes int64
		lastPath := ""
		rootObjects := map[string]bool{}
		for _, member := range publication.Members {
			if member.Path <= lastPath || (member.Path != "index.json" && member.Path != mirrorBlobKey(member.SHA256)) {
				return e.New(e.CodeVerifyFailed, "boot metadata has an unsafe, repeated or unordered member")
			}
			lastPath = member.Path
			if member.Path == "index.json" {
				if member.SHA256 != publication.RootDigest {
					return e.New(e.CodeVerifyFailed, "original boot root index differs from its exact subject")
				}
				seenIndex = true
			}
			object, err := useObject(member.SHA256, "control")
			if err != nil {
				return err
			}
			if !rootObjects[member.SHA256] {
				rootBytes += object.Size
				rootObjects[member.SHA256] = true
			}
			if rootBytes > maxMetadataBytes {
				return e.New(e.CodeEngineCapability, "boot original root exceeds reserved control bytes")
			}
			controlDigests[member.SHA256] = true
		}
		if !seenIndex {
			return e.New(e.CodeVerifyFailed, "boot original root omits its intact index")
		}
	}
	if !rootFound {
		return e.New(e.CodeVerifyFailed, "boot inventory omits original aggregate metadata")
	}
	proofSubjects := map[string]bool{}
	proofObjects := map[string]bool{}
	var proofBytes int64
	previous = ""
	for _, proof := range inventory.Proofs {
		if err := ValidatePublicationReference(proof.Subject.Ref); err != nil {
			return err
		}
		_, digest, _ := strings.Cut(proof.Subject.Ref, "@")
		if proof.Subject.Ref <= previous || digest != proof.Subject.Digest || !controlDigests[digest] {
			return e.New(e.CodeVerifyFailed, "boot original proof has a repeated or unbound subject")
		}
		proofSubjects[proof.Subject.Ref] = true
		previous = proof.Subject.Ref
		for _, digest := range []string{proof.Signature, proof.Provenance} {
			object, err := useObject(digest, "proof")
			if err != nil {
				return err
			}
			if object.Size <= 0 || object.Size > maxMirrorProofBundleBytes {
				return e.New(e.CodeEngineCapability, "boot proof exceeds individual verification bound")
			}
			if !proofObjects[digest] {
				proofBytes += object.Size
				proofObjects[digest] = true
			}
			if proofBytes > maxMirrorProofBytes {
				return e.New(e.CodeEngineCapability, "boot proof set exceeds complete reserved bytes")
			}
		}
	}
	for _, publication := range inventory.Publications {
		if !proofSubjects[publication.Reference] {
			return e.New(e.CodeVerifyFailed, "boot original root omits required signature/provenance proof")
		}
	}
	controlCount := 0
	for _, record := range inventory.Objects {
		derived := make([]string, 0, len(roles[record.SHA256]))
		for role := range roles[record.SHA256] {
			derived = append(derived, role)
		}
		slices.Sort(derived)
		if len(derived) == 0 || !slices.Equal(derived, record.Roles) {
			return e.New(e.CodeVerifyFailed, "boot object is extra or has conflicting logical roles")
		}
		if roles[record.SHA256]["control"] || roles[record.SHA256]["proof"] {
			controlCount++
		}
	}
	if controlCount > limits.ControlObjects {
		return e.New(e.CodeEngineCapability, "boot controls exceed Plan-derived reserved object count")
	}
	// Exact descriptor proof subjects and original metadata membership are checked
	// after sparse restoration and signature authentication by lifecycle.
	return nil
}
