// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/development"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"go.yaml.in/yaml/v3"
)

func objectEnvironment() Environment {
	return Environment{APIVersion: EnvironmentObjectStoresAPIVersion, Kind: EnvironmentKind, Metadata: Metadata{Name: "fixture"}, UtilityContainer: "golang@sha256:" + strings.Repeat("a", 64), UtilityPlatform: "linux/amd64", Signing: &Signing{Mode: "cosign", Identity: "^https://github.com/example/nvkr/.*$", Issuer: "https://token.actions.githubusercontent.com"}, Publication: &Publication{RepositoryPrefix: "ghcr.io/example/nvkr", CredentialRef: "github:REGISTRY", ObjectStores: []ObjectStoreDestination{{Name: "customer", Provider: "s3", S3: &S3ObjectStoreDestination{Region: "us-east-1", Bucket: "synthetic-artifacts", Prefix: "products/nvke", ExpectedBucketOwner: "123456789012", RoleRef: "github:S3_PUBLISH_ROLE"}}}}}
}

func TestObjectStoreEnvironmentAdmission(t *testing.T) {
	namespace, err := development.ParseNamespace("ghcr.io/example/nvkr/development")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*Environment)
		code e.Code
	}{
		{"exact", func(*Environment) {}, ""},
		{"v1", func(x *Environment) { x.APIVersion = EnvironmentAPIVersion }, e.CodeInvalidCatalog},
		{"empty v2", func(x *Environment) { x.Publication.ObjectStores = nil }, e.CodeInvalidCatalog},
		{"provider", func(x *Environment) { x.Publication.ObjectStores[0].Provider = "gcs" }, e.CodeEngineCapability},
		{"development", func(x *Environment) {
			x.GitHubDevelopment = true
			x.Publication.Mode, x.Publication.RepositoryPrefix, x.Publication.CredentialRef = "per-output-v1", namespace.Prefix(), development.WriteCredentialRef
			x.Signing.Identity, x.Signing.Issuer = namespace.Identity(), development.Issuer
		}, e.CodeEngineCapability},
		{"unsigned", func(x *Environment) { x.Signing = nil }, e.CodeEngineCapability},
		{"prefix escape", func(x *Environment) { x.Publication.ObjectStores[0].S3.Prefix = "../other" }, e.CodeInvalidCatalog},
		{"endpoint credentials", func(x *Environment) { x.Publication.ObjectStores[0].S3.Endpoint = "https://user:secret@example.test" }, e.CodeInvalidCatalog},
		{"endpoint path", func(x *Environment) { x.Publication.ObjectStores[0].S3.Endpoint = "https://example.test/" }, e.CodeInvalidCatalog},
		{"endpoint empty query", func(x *Environment) { x.Publication.ObjectStores[0].S3.Endpoint = "https://example.test?" }, e.CodeInvalidCatalog},
		{"endpoint empty fragment", func(x *Environment) { x.Publication.ObjectStores[0].S3.Endpoint = "https://example.test#" }, e.CodeInvalidCatalog},
		{"endpoint invalid UTF8", func(x *Environment) { x.Publication.ObjectStores[0].S3.Endpoint = "https://example.test\xff" }, e.CodeInvalidCatalog},
		{"bucket control", func(x *Environment) { x.Publication.ObjectStores[0].S3.Bucket = "synthetic\x01bucket" }, e.CodeInvalidCatalog},
		{"bucket invalid UTF8", func(x *Environment) { x.Publication.ObjectStores[0].S3.Bucket = "synthetic\xffbucket" }, e.CodeInvalidCatalog},
		{"owner", func(x *Environment) { x.Publication.ObjectStores[0].S3.ExpectedBucketOwner = "unknown" }, e.CodeTrustPolicyMissing},
		{"role value", func(x *Environment) {
			x.Publication.ObjectStores[0].S3.RoleRef = "arn:aws:iam::123456789012:role/publisher"
		}, e.CodeTrustPolicyMissing},
		{"maximum session timeout", func(x *Environment) { x.Publication.ObjectStores[0].Transfer.OperationTimeoutSeconds = 3509 }, ""},
		{"cleanup exceeds session", func(x *Environment) { x.Publication.ObjectStores[0].Transfer.OperationTimeoutSeconds = 3510 }, e.CodeEngineCapability},
		{"one hour omits cleanup", func(x *Environment) { x.Publication.ObjectStores[0].Transfer.OperationTimeoutSeconds = 3600 }, e.CodeEngineCapability},
		{"total only", func(x *Environment) { x.Publication.ObjectStores[0].Transfer.MaxTotalBytes = 1 }, ""},
		{"object capability", func(x *Environment) { x.Publication.ObjectStores[0].Transfer.MaxObjectBytes = 64<<30 + 1 }, e.CodeEngineCapability},
		{"inventory capability", func(x *Environment) { x.Publication.ObjectStores[0].Transfer.MaxObjects = 32769 }, e.CodeEngineCapability},
		{"maximum capabilities", func(x *Environment) {
			x.Publication.ObjectStores[0].Transfer.MaxObjectBytes = 64 << 30
			x.Publication.ObjectStores[0].Transfer.MaxObjects = 32768
		}, ""},
		{"duplicate", func(x *Environment) {
			x.Publication.ObjectStores = append(x.Publication.ObjectStores, x.Publication.ObjectStores[0])
		}, e.CodeInvalidCatalog},
		{"runtime credentials", func(x *Environment) { x.Runtime.Variables = map[string]string{"NVKR_S3_ROLE_ARN": "authored"} }, e.CodeTrustPolicyMissing},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := objectEnvironment()
			tc.edit(&x)
			if tc.name == "development" {
				withoutStore := x.Clone()
				withoutStore.APIVersion, withoutStore.Publication.ObjectStores = EnvironmentAPIVersion, nil
				if err := withoutStore.Validate(); err != nil {
					t.Fatal("development fixture must be valid without S3", err)
				}
			}
			err := x.Validate()
			if tc.code != "" {
				requireCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestObjectStoreTransferDefaultsAndAdmission(t *testing.T) {
	for _, tc := range []struct {
		name       string
		authored   ObjectStoreTransfer
		wantObject uint64
		wantTotal  uint64
		code       e.Code
	}{
		{"omitted", ObjectStoreTransfer{}, 64 << 30, 256 << 30, ""},
		{"explicit defaults", ObjectStoreTransfer{MaxObjectBytes: 64 << 30, MaxTotalBytes: 256 << 30}, 64 << 30, 256 << 30, ""},
		{"total only below object default", ObjectStoreTransfer{MaxTotalBytes: 10 << 30}, 10 << 30, 10 << 30, ""},
		{"total only minimum", ObjectStoreTransfer{MaxTotalBytes: 1}, 1, 1, ""},
		{"total only at object default", ObjectStoreTransfer{MaxTotalBytes: 64 << 30}, 64 << 30, 64 << 30, ""},
		{"total only above object default", ObjectStoreTransfer{MaxTotalBytes: 128 << 30}, 64 << 30, 128 << 30, ""},
		{"object only", ObjectStoreTransfer{MaxObjectBytes: 10 << 30}, 10 << 30, 256 << 30, ""},
		{"explicit equal limits", ObjectStoreTransfer{MaxObjectBytes: 10 << 30, MaxTotalBytes: 10 << 30}, 10 << 30, 10 << 30, ""},
		{"explicit object below total", ObjectStoreTransfer{MaxObjectBytes: 5 << 30, MaxTotalBytes: 10 << 30}, 5 << 30, 10 << 30, ""},
		{"explicit object above total", ObjectStoreTransfer{MaxObjectBytes: 64 << 30, MaxTotalBytes: 10 << 30}, 64 << 30, 10 << 30, e.CodeEngineCapability},
	} {
		t.Run(tc.name, func(t *testing.T) {
			environment := objectEnvironment()
			environment.Publication.ObjectStores[0].Transfer = tc.authored
			before, err := canonical.Marshal(environment)
			if err != nil {
				t.Fatal(err)
			}
			beforeDigest, err := canonical.Digest(canonical.KindEnvironment, environment)
			if err != nil {
				t.Fatal(err)
			}
			got := environment.Publication.ObjectStores[0].EffectiveTransfer()
			want := ObjectStoreTransfer{MaxObjectBytes: tc.wantObject, MaxTotalBytes: tc.wantTotal, MaxObjects: 8192, OperationTimeoutSeconds: 1200}
			if got != want {
				t.Errorf("effective transfer: got %+v, want %+v", got, want)
			}
			err = environment.Validate()
			if tc.code == "" {
				if err != nil {
					t.Error(err)
				}
				afterDigest, err := environment.Digest()
				if err != nil || afterDigest != beforeDigest {
					t.Errorf("defaulting changed Environment identity: before=%s after=%s err=%v", beforeDigest, afterDigest, err)
				}
			} else {
				requireCode(t, err, tc.code)
			}
			after, err := canonical.Marshal(environment)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatalf("defaulting changed authored bytes: %v", err)
			}
		})
	}
}

func TestObjectStoreEnvironmentVersionAndClone(t *testing.T) {
	x := objectEnvironment()
	raw, err := canonical.Marshal(x)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeEnvironment(raw)
	if err != nil {
		t.Fatal(err)
	}
	again, err := canonical.Marshal(decoded)
	if err != nil || !bytes.Equal(raw, again) {
		t.Fatal("object destination changed during strict roundtrip")
	}
	clone := x.Clone()
	clone.Publication.ObjectStores[0].S3.Prefix = "other"
	if x.Publication.ObjectStores[0].S3.Prefix != "products/nvke" {
		t.Fatal("clone aliases destination")
	}
	x.APIVersion = EnvironmentAPIVersion
	x.Publication.ObjectStores = nil
	old, err := canonical.Marshal(x)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(old, []byte("objectStores")) {
		t.Fatal("omitted destination changed old wire bytes")
	}
	for _, value := range []string{"null", "[]", "[{}]"} {
		bad := bytes.Replace(old, []byte(`"publication":{`), []byte(`"publication":{"objectStores":`+value+`,`), 1)
		_, err := DecodeEnvironment(bad)
		requireCode(t, err, e.CodeInvalidCatalog)
	}
	limits := objectEnvironment().Publication.ObjectStores[0].EffectiveTransfer()
	if limits.OperationTimeoutSeconds != 1200 || limits.MaxObjectBytes != 64<<30 || limits.MaxTotalBytes != 256<<30 || limits.MaxObjects != 8192 {
		t.Fatal("transfer defaults changed")
	}
}

func TestObjectStoreOmissionPreservesV1NativeYAMLRoundTrip(t *testing.T) {
	environment, err := LoadEnvironment(t.Context(), "../../../inputs/environments/github-release.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if environment.APIVersion != EnvironmentAPIVersion || environment.Publication == nil || environment.Publication.ObjectStores != nil {
		t.Fatal("roundtrip requires the maintained v1 publication input")
	}
	environment.Publication.Transports = []string{PublicationTransportOCI}
	before, err := environment.Digest()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := yaml.Marshal(environment)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bytes.ToLower(raw), []byte("objectstores:")) {
		t.Fatal("native YAML marshaling added the v2 field to a v1 input")
	}
	decoded, err := DecodeEnvironment(raw)
	if err != nil {
		t.Fatal(err)
	}
	after, err := decoded.Digest()
	if err != nil || before != after {
		t.Fatalf("native YAML roundtrip changed v1 identity: before=%s after=%s err=%v", before, after, err)
	}
}
