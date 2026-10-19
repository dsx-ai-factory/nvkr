// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package release

import (
	"context"
	"errors"
	"strings"
	"testing"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/trust"
)

type statusDocumentVerifier struct {
	signer string
	issuer string
}

func (verifier statusDocumentVerifier) VerifyDocument(context.Context, trust.DocumentVerificationRequest) (trust.VerificationEvidence, error) {
	return trust.VerificationEvidence{
		CertificateIdentities: []string{verifier.signer}, OIDCIssuer: verifier.issuer,
		CertificateSHA256: digest('a'),
	}, nil
}

func code(err error) nvkrerr.Code {
	var coded *nvkrerr.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

func digest(char byte) string { return "sha256:" + strings.Repeat(string(char), 64) }

func inventory() *Inventory {
	return &Inventory{
		SchemaVersion: InventorySchemaVersion,
		Version:       "1.0",
		Components: []InventoryComponent{
			{Name: "kubelet", VersionConstraint: "1.32.x", RequiredArtifacts: []string{digest('a')},
				ArtifactKinds: []ArtifactKindBinding{{ArtifactDigest: digest('a'), Kind: ArtifactHostPackage}}},
			{Name: "apiserver", VersionConstraint: ">=1.32 <1.33", RequiredArtifacts: []string{digest('b')},
				ArtifactKinds: []ArtifactKindBinding{{ArtifactDigest: digest('b'), Kind: ArtifactHostPackage}}},
		},
		Compatibility: Compatibility{Kubernetes: "1.32.x", NVKE: ">=2.4 <3.0", Platform: "bare-metal|eks"},
	}
}

func releaseSet(t *testing.T, inv *Inventory) *ReleaseSet {
	t.Helper()
	invDigest, err := inv.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return &ReleaseSet{
		SchemaVersion: ReleaseSetSchemaVersion,
		Version:       "1.0",
		InventoryRef:  invDigest,
		Members: []Member{
			{
				Component: "kubelet", Arch: "amd64", Version: "1.32.7",
				BuildDigest: digest('c'), VectorDigest: digest('d'), ResolvedInputDigest: digest('3'),
				ArtifactDigests: []string{digest('a')},
			},
			{
				Component: "apiserver", Arch: "amd64", Version: "1.32.7",
				BuildDigest: digest('f'), VectorDigest: digest('1'), ResolvedInputDigest: digest('5'),
				ArtifactDigests: []string{digest('b')},
			},
		},
	}
}

func TestReleaseDocumentsRejectNonCanonicalAndEmptyIdentityFields(t *testing.T) {
	for name, mutate := range map[string]func(*Inventory){
		"version whitespace": func(value *Inventory) { value.Version = " 1.0" },
		"member whitespace":  func(value *Inventory) { value.Components[0].Name = "kubelet " },

		"no artifacts": func(value *Inventory) {
			value.Components[0].RequiredArtifacts = nil
			value.Components[0].ArtifactKinds = nil
		},
	} {
		t.Run("inventory "+name, func(t *testing.T) {
			value := inventory()
			mutate(value)
			if err := value.Validate(); err == nil {
				t.Fatal("invalid inventory identity must fail closed")
			}
		})
	}
	baseInventory := inventory()
	for name, mutate := range map[string]func(*ReleaseSet){
		"version whitespace": func(value *ReleaseSet) { value.Version = "1.0 " },
		"component whitespace": func(value *ReleaseSet) {
			value.Members[0].Component = " kubelet"
		},
		"unsupported arch": func(value *ReleaseSet) { value.Members[0].Arch = "s390x" },
		"no artifacts":     func(value *ReleaseSet) { value.Members[0].ArtifactDigests = nil },
	} {
		t.Run("release set "+name, func(t *testing.T) {
			value := releaseSet(t, baseInventory)
			mutate(value)
			if err := value.Validate(); err == nil {
				t.Fatal("invalid release-set identity must fail closed")
			}
		})
	}
}

func TestCompletenessNamesMissingMemberAndArtifactWithoutTrustingRawEvidence(t *testing.T) {
	inv := inventory()
	set := releaseSet(t, inv)
	set.Members = set.Members[:1]
	set.Members[0].ArtifactDigests = []string{digest('9')}
	err := set.CompletenessGate(inv)
	if code(err) != nvkrerr.CodeReleaseIncomplete {
		t.Fatalf("incomplete release must fail closed: %v", err)
	}
	for _, want := range []string{"apiserver", "artifact " + digest('a')} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("completeness error must name %q: %v", want, err)
		}
	}
}
