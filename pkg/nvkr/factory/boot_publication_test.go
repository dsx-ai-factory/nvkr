// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestBootPublicationCannotEnterLegacyGraph(t *testing.T) {
	for _, tc := range []struct {
		name    string
		unknown bool
		code    e.Code
	}{
		{name: "new route", code: e.CodeEngineCapability},
		{name: "unknown selected output", unknown: true, code: e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := definition.Definition{APIVersion: definition.DefinitionAPIVersion, Kind: definition.DefinitionKind,
				Metadata: definition.Metadata{Name: "selected"}, Catalog: definition.Catalog{Base: "not-read"},
				Outputs: []definition.DefinitionOutput{
					{Name: "imager", Format: definition.OutputExecutable, Platform: "linux/amd64", Components: []string{"imager.efi"}},
					{Name: "node", Format: definition.OutputDiskImage, Platform: "linux/amd64", Components: []string{"node"}},
				},
			}
			environment := definition.Environment{APIVersion: definition.EnvironmentBootAPIVersion, Kind: definition.EnvironmentKind,
				Metadata: definition.Metadata{Name: "selected"}, UtilityContainer: "golang@sha256:" + strings.Repeat("a", 64), UtilityPlatform: "linux/amd64",
				Signing: &definition.Signing{Mode: "cosign", Identity: "^https://github.com/example/nvkr/.*$", Issuer: "https://token.actions.githubusercontent.com"},
				Publication: &definition.Publication{Mode: "per-output-v1", RepositoryPrefix: "ghcr.io/example/nvkr", CredentialRef: "github:REGISTRY",
					BootSelection: &definition.BootSelection{SchemaVersion: definition.BootSelectionSchemaVersion,
						Disks:   []definition.BootDisk{{Output: "node", Platform: "linux/amd64"}},
						Imagers: []definition.BootImager{{Output: "imager", Member: "imager.efi", Platform: "linux/amd64", Profile: "synthetic-v1"}}},
					ObjectStores: []definition.ObjectStoreDestination{{Name: "boot", Mode: definition.ObjectStoreBootMode, Provider: "s3",
						S3:      &definition.S3ObjectStoreDestination{Region: "us-east-1", Bucket: "synthetic-boot", Prefix: "boot", ExpectedBucketOwner: "123456789012", RoleRef: "github:S3_ROLE"},
						Serving: &definition.BootServing{Profile: definition.BootServingProfile, Origin: "https://boot.example.test", Prefix: "/", ReaderMode: "anonymous"}}},
				},
			}
			if tc.unknown {
				environment.Publication.BootSelection.Disks[0].Output = "sibling"
			}
			calls := 0
			plan, err := compileDocumentsWithLoader(t.Context(), "definition.yaml", input, "environment.yaml", environment, "test",
				func(context.Context, definition.Catalog) (*catalog.Stack, error) { calls++; return nil, nil })
			assertFactoryErrorCode(t, err, tc.code)
			if plan != nil || calls != 0 {
				t.Fatal("selective inputs reached catalog resolution or legacy compilation")
			}
		})
	}
}
