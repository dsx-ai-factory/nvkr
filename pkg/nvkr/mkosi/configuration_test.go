// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package mkosi

import (
	"errors"
	"os"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestConfigurationRejectsAmbientInputs(t *testing.T) {
	for _, raw := range []string{
		"[Output]\nFormat=oci\n[Config]\nInclude=/etc/mkosi.conf\n",
		"[Output]\nFormat=oci\n[Content]\nBuildScripts=/host/script\n",
		"[Output]\nFormat=oci\n[Build]\nBuildSources=/host/tree\n",
		"[Output]\nFormat=oci\n[Content]\nPackages=unpinned\n",
		"[Output]\nFormat=oci\n[Content]\nExtraTrees=/host/root\n",
		"[Output]\nFormat=oci\nFormat=directory\n",
		"[Output]\nFormat=oci\n[Output]\nFormat=oci\n",
		"[Output]\nFormat=oci\n[Content]\nRemoveFiles=/var/lib/dpkg\n",
		"[Output]\nFormat=oci\n[Content]\nSourceDateEpoch=123\n",
		"[Output]\nFormat=oci\n[Content]\nWithRecommends=yes\n",
		"[Output]\nFormat=oci\n arbitrary-continuation\n",
		"[Output]\nFormat=oci\n[Match]\nArchitecture=x86-64\n",
	} {
		err := validateConfiguration(raw)
		var coded *e.Error
		if !errors.As(err, &coded) || coded.Code != e.CodeInvalidCatalog {
			t.Fatalf("ambient configuration accepted: %q, %v", raw, err)
		}
	}
}

func TestCheckedInRuntimeConfiguration(t *testing.T) {
	raw, err := os.ReadFile("../../../toolchains/nvkr-vm-runtime/mkosi.conf")
	if err != nil {
		t.Fatal(err)
	}
	if err := validateConfiguration(string(raw)); err != nil {
		t.Fatal(err)
	}
	if err := validateConfiguration(strings.ReplaceAll(string(raw), "\n", "\r\n")); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeProjectionRejectsConfigurationDelimiters(t *testing.T) {
	for _, location := range []string{"/opt/a:b", "/opt/a,b", "/opt/a b", "/opt/a\tb", "/opt/a\nb", "/opt/%x", "/opt/../root"} {
		if projectionPath(location) {
			t.Fatalf("ambiguous projection admitted: %q", location)
		}
	}
}
