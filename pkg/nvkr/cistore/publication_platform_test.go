// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"fmt"
	"net/http"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func platformPackage(packageName string) map[string]any {
	p := publicationPackage(packageName)
	p["repository"] = map[string]any{"id": 1, "full_name": "Example/Source"}
	return p
}

func TestPlatformPublicationPackagePolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		public bool
		status int
		edit   func(map[string]any)
		code   e.Code
	}{
		{name: "private"},
		{name: "internal", edit: func(p map[string]any) { p["visibility"] = "internal" }},
		{name: "public default refused", edit: func(p map[string]any) { p["visibility"] = "public" }, code: e.CodeTrustPolicyMissing},
		{name: "public opted in", public: true, edit: func(p map[string]any) { p["visibility"] = "public" }},
		{name: "private with opt in", public: true},
		{name: "missing visibility", public: true, edit: func(p map[string]any) { delete(p, "visibility") }, code: e.CodeTrustPolicyMissing},
		{name: "unknown visibility", public: true, edit: func(p map[string]any) { p["visibility"] = "future" }, code: e.CodeTrustPolicyMissing},
		{name: "unlinked", edit: func(p map[string]any) { delete(p, "repository") }, code: e.CodeTrustPolicyMissing},
		{name: "null linkage", edit: func(p map[string]any) { p["repository"] = nil }, code: e.CodeTrustPolicyMissing},
		{name: "wrong repository id", edit: func(p map[string]any) { p["repository"] = map[string]any{"id": 2, "full_name": "Example/Source"} }, code: e.CodeTrustPolicyMissing},
		{name: "wrong repository name", edit: func(p map[string]any) { p["repository"] = map[string]any{"id": 1, "full_name": "Example/Other"} }, code: e.CodeTrustPolicyMissing},
		{name: "substituted package", edit: func(p map[string]any) { p["name"] = "source/other" }, code: e.CodeTrustPolicyMissing},
		{name: "substituted owner", edit: func(p map[string]any) { p["owner"] = map[string]any{"login": "Other"} }, code: e.CodeTrustPolicyMissing},
		{name: "substituted type", edit: func(p map[string]any) { p["package_type"] = "npm" }, code: e.CodeTrustPolicyMissing},
		{name: "missing package no seed", status: http.StatusNotFound, code: e.CodeTrustPolicyMissing},
		{name: "unreadable", status: http.StatusUnauthorized, code: e.CodeFetchFailed},
		// Provider metadata does not expose these settings. No assertion or
		// unknown field is used as proof; their setup is the operator's duty.
		{name: "role and inheritance are operator owned", edit: func(p map[string]any) { p["role"], p["inherit"] = "admin", true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := publicationBackend(t, func(packageName string, _ int) (int, any) {
				p := platformPackage(packageName)
				if tc.edit != nil {
					tc.edit(p)
				}
				status := tc.status
				if status == 0 {
					status = http.StatusOK
				}
				return status, p
			})
			err := f.b.admitPublication(t.Context(), "Example/Source", []string{"ghcr.io/example/source/releases"}, PublicationOptions{
				Options: publisherOptions, PlatformToken: true, AllowPublicPackages: tc.public,
			})
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				assertCode(t, err, tc.code)
			}
			if f.transport.calls.Load() != 0 {
				t.Fatal("platform admission performed a registry operation")
			}
		})
	}
}

func TestPlatformPublicationChecksWholeSetAndRechecksVisibility(t *testing.T) {
	for _, destination := range []string{"source/output", "source/output-evidence", "source/releases"} {
		for _, at := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/check-%d", destination, at), func(t *testing.T) {
				f, _ := publicationBackend(t, func(packageName string, count int) (int, any) {
					p := platformPackage(packageName)
					if packageName == destination && count >= at {
						p["visibility"] = "public"
					}
					return http.StatusOK, p
				})
				err := f.b.admitPublication(t.Context(), "Example/Source", []string{"ghcr.io/example/source/releases", "ghcr.io/example/source/output", "ghcr.io/example/source/output-evidence"}, PublicationOptions{Options: publisherOptions, PlatformToken: true})
				assertCode(t, err, e.CodeTrustPolicyMissing)
				if f.transport.calls.Load() != 0 {
					t.Fatal("rejected destination performed registry IO")
				}
			})
		}
	}
}

func TestPlatformPublicationRejectsForeignNamespaceBeforeIO(t *testing.T) {
	f, calls := publicationBackend(t, func(string, int) (int, any) { return http.StatusNotFound, nil })
	err := f.b.admitPublication(t.Context(), "Example/Source", []string{"ghcr.io/example/other/releases"}, PublicationOptions{Options: publisherOptions, PlatformToken: true})
	assertCode(t, err, e.CodeUsage)
	if calls.Load() != 0 || f.transport.calls.Load() != 0 {
		t.Fatal("foreign destination performed IO")
	}
}

func TestIndependentPublicationPublicOptInCannotRelaxPolicy(t *testing.T) {
	for _, public := range []bool{false, true} {
		f, _ := publicationBackend(t, func(packageName string, _ int) (int, any) {
			p := publicationPackage(packageName)
			p["visibility"] = "public"
			return http.StatusOK, p
		})
		err := f.b.admitPublication(t.Context(), "Example/Source", []string{"ghcr.io/example/source/releases"}, PublicationOptions{Options: publisherOptions, AllowPublicPackages: public})
		assertCode(t, err, e.CodeTrustPolicyMissing)
		if f.transport.calls.Load() != 0 {
			t.Fatal("independent public destination performed registry IO")
		}
	}
}
