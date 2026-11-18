// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestBootServingAdmission(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*BootServing)
		code e.Code
	}{
		{"anonymous", func(*BootServing) {}, ""},
		{"origin root", func(s *BootServing) { s.Prefix = "/" }, ""},
		{"explicit port", func(s *BootServing) { s.Origin = "https://boot.example.test:8443" }, ""},
		{"IPv4", func(s *BootServing) { s.Origin = "https://192.0.2.1" }, ""},
		{"IPv6", func(s *BootServing) { s.Origin = "https://[2001:db8::1]" }, ""},
		{"host punctuation", func(s *BootServing) { s.Origin = "https://a'$(x).example" }, e.CodeInvalidCatalog},
		{"host underscore", func(s *BootServing) { s.Origin = "https://boot_host.example" }, e.CodeInvalidCatalog},
		{"host trailing dot", func(s *BootServing) { s.Origin = "https://boot.example.test." }, e.CodeInvalidCatalog},
		{"host empty label", func(s *BootServing) { s.Origin = "https://boot..example.test" }, e.CodeInvalidCatalog},
		{"host leading hyphen", func(s *BootServing) { s.Origin = "https://-boot.example.test" }, e.CodeInvalidCatalog},
		{"invalid IPv4", func(s *BootServing) { s.Origin = "https://999.0.0.1" }, e.CodeInvalidCatalog},
		{"bracketed hostname", func(s *BootServing) { s.Origin = "https://[boot.example.test]" }, e.CodeInvalidCatalog},
		{"bracketed IPv4", func(s *BootServing) { s.Origin = "https://[192.0.2.1]" }, e.CodeInvalidCatalog},
		{"expanded IPv6", func(s *BootServing) { s.Origin = "https://[2001:db8:0:0:0:0:0:1]" }, e.CodeInvalidCatalog},
		{"default port alias", func(s *BootServing) { s.Origin += ":443" }, e.CodeInvalidCatalog},
		{"private reader", func(s *BootServing) { s.ReaderMode, s.CredentialRef = "bearer", "github:BOOT_READER" }, ""},
		{"unknown profile", func(s *BootServing) { s.Profile = "other" }, e.CodeEngineCapability},
		{"missing reader", func(s *BootServing) { s.ReaderMode = "" }, e.CodeEngineCapability},
		{"unknown reader", func(s *BootServing) { s.ReaderMode = "inherit-writer" }, e.CodeEngineCapability},
		{"missing reference", func(s *BootServing) { s.ReaderMode = "bearer" }, e.CodeTrustPolicyMissing},
		{"literal credential", func(s *BootServing) { s.ReaderMode, s.CredentialRef = "bearer", "synthetic-secret" }, e.CodeTrustPolicyMissing},
		{"reserved credential", func(s *BootServing) { s.ReaderMode, s.CredentialRef = "bearer", "github:GITHUB_TOKEN" }, e.CodeTrustPolicyMissing},
		{"anonymous credential", func(s *BootServing) { s.CredentialRef = "github:BOOT_READER" }, e.CodeTrustPolicyMissing},
		{"http", func(s *BootServing) { s.Origin = "http://boot.example.test" }, e.CodeInvalidCatalog},
		{"userinfo", func(s *BootServing) { s.Origin = "https://synthetic:secret@boot.example.test" }, e.CodeInvalidCatalog},
		{"empty query", func(s *BootServing) { s.Origin += "?" }, e.CodeInvalidCatalog},
		{"presigned", func(s *BootServing) { s.Origin += "?X-Amz-Signature=synthetic" }, e.CodeInvalidCatalog},
		{"empty fragment", func(s *BootServing) { s.Origin += "#" }, e.CodeInvalidCatalog},
		{"origin path", func(s *BootServing) { s.Origin += "/boot" }, e.CodeInvalidCatalog},
		{"origin slash", func(s *BootServing) { s.Origin += "/" }, e.CodeInvalidCatalog},
		{"host case", func(s *BootServing) { s.Origin = "https://Boot.example.test" }, e.CodeInvalidCatalog},
		{"empty port", func(s *BootServing) { s.Origin += ":" }, e.CodeInvalidCatalog},
		{"invalid port", func(s *BootServing) { s.Origin += ":65536" }, e.CodeInvalidCatalog},
		{"padded port", func(s *BootServing) { s.Origin += ":0443" }, e.CodeInvalidCatalog},
		{"unicode host", func(s *BootServing) { s.Origin = "https://böot.example.test" }, e.CodeInvalidCatalog},
		{"empty prefix", func(s *BootServing) { s.Prefix = "" }, e.CodeInvalidCatalog},
		{"relative prefix", func(s *BootServing) { s.Prefix = "boot" }, e.CodeInvalidCatalog},
		{"dot prefix", func(s *BootServing) { s.Prefix = "/." }, e.CodeInvalidCatalog},
		{"traversal", func(s *BootServing) { s.Prefix = "/boot/../other" }, e.CodeInvalidCatalog},
		{"double slash", func(s *BootServing) { s.Prefix = "//boot" }, e.CodeInvalidCatalog},
		{"trailing slash", func(s *BootServing) { s.Prefix = "/boot/" }, e.CodeInvalidCatalog},
		{"encoded path", func(s *BootServing) { s.Prefix = "/boot%2Fother" }, e.CodeInvalidCatalog},
		{"template", func(s *BootServing) { s.Prefix = "/boot/{digest}" }, e.CodeInvalidCatalog},
		{"query prefix", func(s *BootServing) { s.Prefix = "/boot?key=" }, e.CodeInvalidCatalog},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serving := BootServing{Profile: BootServingProfile, Origin: "https://boot.example.test", Prefix: "/products/nvke", ReaderMode: "anonymous"}
			tc.edit(&serving)
			err := serving.Validate()
			if tc.code != "" {
				requireCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
