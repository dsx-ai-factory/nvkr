// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"errors"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestToolchainAccessClassification(t *testing.T) {
	access := &ToolchainAccess{PrivateRoots: []string{"ghcr.io/example/tools"}, PublicRoots: []string{"docker.io/library/golang"}}
	classifier, err := access.ImageClassifier()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, image string
		private     bool
		code        e.Code
	}{
		{"private repository", "ghcr.io/example/tools", true, ""},
		{"private child", "ghcr.io/example/tools/packer", true, ""},
		{"public shorthand", "golang", false, ""},
		{"public explicit", "docker.io/library/golang", false, ""},
		{"public canonical", "index.docker.io/library/golang", false, ""},
		{"foreign owner", "ghcr.io/foreign/tools/packer", false, e.CodeEngineCapability},
		{"owner lookalike", "ghcr.io/example-other/tools/packer", false, e.CodeEngineCapability},
		{"root lookalike", "ghcr.io/example/tools-other/packer", false, e.CodeEngineCapability},
		{"parent", "ghcr.io/example", false, e.CodeEngineCapability},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, classify := range []func(string) (bool, error){access.ClassifyImage, classifier.ClassifyImage} {
				private, err := classify(test.image + "@sha256:" + strings.Repeat("a", 64))
				if test.code != "" {
					if !toolchainErrorCode(err, test.code) || private {
						t.Fatalf("got private=%t, %v", private, err)
					}
				} else if err != nil || private != test.private {
					t.Fatalf("got private=%t, %v", private, err)
				}
			}
		})
	}
	for _, image := range []string{"ghcr.io/example/tools:latest", "ghcr.io/example/tools@sha256:abc", "sha256:" + strings.Repeat("a", 64)} {
		for _, classify := range []func(string) (bool, error){access.ClassifyImage, classifier.ClassifyImage} {
			if _, err := classify(image); !toolchainErrorCode(err, e.CodeUnpinnedInput) {
				t.Fatalf("unpinned image: %v", err)
			}
		}
	}
}

func TestToolchainImageClassifierOwnsValidatedRoots(t *testing.T) {
	access := &ToolchainAccess{PrivateRoots: []string{"ghcr.io/example/tools"}, PublicRoots: []string{"docker.io/library/golang"}}
	classifier, err := access.ImageClassifier()
	if err != nil {
		t.Fatal(err)
	}
	access.PrivateRoots[0] = "ghcr.io/foreign/tools"
	access.PublicRoots[0] = "ghcr.io/foreign/tools"
	for _, test := range []struct {
		image   string
		private bool
		code    e.Code
	}{
		{"ghcr.io/example/tools/packer", true, ""},
		{"golang", false, ""},
		{"ghcr.io/foreign/tools/packer", false, e.CodeEngineCapability},
	} {
		private, err := classifier.ClassifyImage(test.image + "@sha256:" + strings.Repeat("a", 64))
		if test.code == "" {
			if err != nil || private != test.private {
				t.Fatalf("snapshot changed for %s: %t, %v", test.image, private, err)
			}
		} else if !toolchainErrorCode(err, test.code) || private {
			t.Fatalf("snapshot admitted undeclared image: %t, %v", private, err)
		}
	}
	image := "ghcr.io/example/tools/packer@sha256:" + strings.Repeat("a", 64)
	if _, err := access.ClassifyImage(image); !toolchainErrorCode(err, e.CodeInvalidCatalog) {
		t.Fatalf("exported classifier reused stale validation: %v", err)
	}
	invalid, err := access.ImageClassifier()
	if !toolchainErrorCode(err, e.CodeInvalidCatalog) {
		t.Fatalf("invalid declaration returned a classifier: %v", err)
	}
	if private, err := invalid.ClassifyImage(image); !toolchainErrorCode(err, e.CodeEngineCapability) || private {
		t.Fatalf("zero classifier did not fail closed: %t, %v", private, err)
	}
}

func TestToolchainAccessDeclarationAdmission(t *testing.T) {
	for _, test := range []struct {
		name   string
		access ToolchainAccess
		code   e.Code
	}{
		{"empty", ToolchainAccess{}, ""},
		{"missing reader deferred to selection", ToolchainAccess{PrivateRoots: []string{"ghcr.io/example/tools"}}, ""},
		{"overlapping classes", ToolchainAccess{PrivateRoots: []string{"ghcr.io/example/tools"}, PublicRoots: []string{"ghcr.io/example/tools/packer"}}, e.CodeInvalidCatalog},
		{"overlapping private", ToolchainAccess{PrivateRoots: []string{"ghcr.io/example/tools", "ghcr.io/example/tools/packer"}}, e.CodeInvalidCatalog},
		{"docker aliases", ToolchainAccess{PublicRoots: []string{"docker.io/library/golang", "index.docker.io/library/golang"}}, e.CodeInvalidCatalog},
		{"duplicate", ToolchainAccess{PublicRoots: []string{"ghcr.io/example/tools", "ghcr.io/example/tools"}}, e.CodeInvalidCatalog},
		{"tagged root", ToolchainAccess{PrivateRoots: []string{"ghcr.io/example/tools:latest"}}, e.CodeInvalidCatalog},
		{"trailing slash", ToolchainAccess{PrivateRoots: []string{"ghcr.io/example/tools/"}}, e.CodeInvalidCatalog},
		{"shorthand root", ToolchainAccess{PublicRoots: []string{"golang"}}, e.CodeInvalidCatalog},
		{"traversal", ToolchainAccess{PrivateRoots: []string{"ghcr.io/example/tools/../foreign"}}, e.CodeInvalidCatalog},
		{"empty path component", ToolchainAccess{PrivateRoots: []string{"ghcr.io/example//tools"}}, e.CodeInvalidCatalog},
		{"too many roots", ToolchainAccess{PublicRoots: make([]string, 129)}, e.CodeInvalidCatalog},
		{"foreign registry", ToolchainAccess{PrivateRoots: []string{"example.test/tools"}}, e.CodeEngineCapability},
		{"reader without roots", ToolchainAccess{Reader: &ToolchainReader{CredentialRef: "github:READER", UsernameRef: "github:ACCOUNT"}}, e.CodeInvalidCatalog},
		{"missing username", ToolchainAccess{PrivateRoots: []string{"ghcr.io/example/tools"}, Reader: &ToolchainReader{CredentialRef: "github:READER"}}, e.CodeInvalidCatalog},
		{"ambient token", ToolchainAccess{PrivateRoots: []string{"ghcr.io/example/tools"}, Reader: &ToolchainReader{CredentialRef: "github:GITHUB_TOKEN", UsernameRef: "github:ACCOUNT"}}, e.CodeInvalidCatalog},
		{"expression injection", ToolchainAccess{PrivateRoots: []string{"ghcr.io/example/tools"}, Reader: &ToolchainReader{CredentialRef: "github:READER }}", UsernameRef: "github:ACCOUNT"}}, e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.access.Validate()
			if test.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !toolchainErrorCode(err, test.code) {
				t.Fatalf("expected %s: %v", test.code, err)
			}
		})
	}
}

func TestToolchainAccessClone(t *testing.T) {
	original := Environment{ToolchainAccess: &ToolchainAccess{PrivateRoots: []string{"ghcr.io/example/tools"}, PublicRoots: []string{"docker.io/library/golang"}, Reader: &ToolchainReader{CredentialRef: "github:READER", UsernameRef: "github:ACCOUNT"}}}
	copy := original.Clone()
	copy.ToolchainAccess.PrivateRoots[0] = "ghcr.io/foreign/tools"
	copy.ToolchainAccess.PublicRoots[0] = "docker.io/library/busybox"
	copy.ToolchainAccess.Reader.CredentialRef = "github:OTHER"
	if original.ToolchainAccess.PrivateRoots[0] != "ghcr.io/example/tools" || original.ToolchainAccess.PublicRoots[0] != "docker.io/library/golang" || original.ToolchainAccess.Reader.CredentialRef != "github:READER" {
		t.Fatal("clone shares access declaration")
	}
}

func TestEnvironmentToolchainReaderExcludesWriters(t *testing.T) {
	for _, test := range []struct {
		name, reader, publication string
		valid                     bool
	}{
		{"independent reader", "github:READER", "github:PUBLISHER", true},
		{"reader without publication", "github:READER", "", true},
		{"publication credential", "github:PUBLISHER", "github:PUBLISHER", false},
		{"publication credential case", "github:publisher", "github:PUBLISHER", false},
		{"production writer", "github:NVKR_REGISTRY_WRITE", "", false},
		{"production writer case", "github:nvkr_registry_write", "", false},
		{"development writer", "github:NVKR_DEV_REGISTRY_WRITE", "", false},
		{"development writer case", "github:nvkr_dev_registry_write", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment := Environment{
				APIVersion: EnvironmentAPIVersion, Kind: EnvironmentKind, Metadata: Metadata{Name: "fixture"},
				UtilityContainer: "golang@sha256:" + strings.Repeat("a", 64), UtilityPlatform: "linux/amd64",
				ToolchainAccess: &ToolchainAccess{PrivateRoots: []string{"ghcr.io/example/tools"},
					Reader: &ToolchainReader{CredentialRef: test.reader, UsernameRef: "github:ACCOUNT"}},
			}
			if test.publication != "" {
				environment.Publication = &Publication{RepositoryPrefix: "ghcr.io/example/output", CredentialRef: test.publication}
			}
			err := environment.Validate()
			if test.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else if !toolchainErrorCode(err, e.CodeInvalidCatalog) || !strings.Contains(err.Error(), "publication writer") {
				t.Fatalf("expected writer isolation refusal, got %v", err)
			}
		})
	}
}

func toolchainErrorCode(err error, code e.Code) bool {
	var coded *e.Error
	return errors.As(err, &coded) && coded.Code == code
}
