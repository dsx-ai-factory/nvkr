// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func TestMaintenanceRejectsRepublishedSourceBeforeCommit(t *testing.T) {
	for _, tc := range []struct {
		name, tag            string
		strict, check, moved bool
		code                 e.Code
	}{
		{"stable", "v1.0.0", true, false, false, ""},
		{"moved exact tag", "v1.0.0", true, false, true, e.CodeUnverifiedRef},
		{"moved exact preview", "v1.0.0", true, true, true, e.CodeUnverifiedRef},
		{"same version alias", "1.0.0", true, false, true, e.CodeUnverifiedRef},
		{"new version", "v1.1.0", true, false, true, ""},
		{"interactive explicit repin", "v1.0.0", false, false, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := sourceUpdateFixture(t)
			before := readPinFixtureLock(t, dir)
			update, err := updateSourcePins(t.Context(), SourcePinRequest{Directory: dir, Sources: []string{"source"}, Bump: true, Check: tc.check, RejectRepublishedVersions: tc.strict},
				func(context.Context, string) ([]string, error) { return []string{tc.tag}, nil },
				func(context.Context, Source, string) (Pin, error) {
					pin := sourceUpdatePin(tc.tag)
					if tc.moved {
						pin.Commit = strings.Repeat("c", 40)
					}
					return pin, nil
				})
			if tc.code != "" {
				assertCatalogContractCode(t, err, tc.code)
			} else if err != nil {
				t.Fatal(err)
			}
			wantWrite := tc.moved && tc.code == ""
			if bytes.Equal(before, readPinFixtureLock(t, dir)) == wantWrite {
				t.Fatalf("wrong write boundary: %+v %v", update, err)
			}
		})
	}
}

func TestMaintenanceRejectsRepublishedAcquiredMemberAtomically(t *testing.T) {
	var changed atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := r.URL.Path
		if changed.Load() {
			payload += "republished"
		}
		_, _ = fmt.Fprint(w, strings.TrimPrefix(pinFixtureDigest(payload), "sha256:"))
	}))
	t.Cleanup(server.Close)
	original := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = original })
	fixture := newPinSetIntegrationFixture(t, "dependencies: [{name: cwe, version: 1.2.3}, {name: tool, version: 2.0.0}]\n")
	fixture.registry.Sources["umbrella"].PinSet.Members["b-tool"] = PinSetMember{Artifact: "tool", Path: "dependencies[name=tool].version"}
	fixture.registry.Components["tool"] = rawComponent{Platforms: []string{"linux/amd64", "linux/arm64"}, PinnedBy: "umbrella", OutputType: "raw", Prebuilt: &Prebuilt{URL: server.URL + "/tool/{{.Version}}/{{.Arch}}", Evidence: &ExternalEvidence{None: true}}}
	fixture.writeRegistry(t)
	fixture.pin(t, "1.0.0")
	// The newer umbrella retains the exact source and acquired member versions.
	fixture.commitManifest(t, "dependencies: [{name: cwe, version: 1.2.3}, {name: tool, version: 2.0.0}]\n# refreshed umbrella\n", "umbrella/v1.1.0")
	before := readPinFixtureLock(t, fixture.dir)
	changed.Store(true)
	for _, check := range []bool{true, false} {
		request := SourcePinRequest{Directory: fixture.dir, Sources: []string{"umbrella"}, Bump: true, Check: check, RejectRepublishedVersions: true}
		_, err := UpdateSourcePins(t.Context(), request)
		assertCatalogContractCode(t, err, e.CodeUnverifiedRef)
		if !bytes.Equal(before, readPinFixtureLock(t, fixture.dir)) {
			t.Fatal("republished acquired bytes committed a partial umbrella or member")
		}
	}
	// Advancing only derived provenance with unchanged member bytes is allowed.
	changed.Store(false)
	_, err := UpdateSourcePins(t.Context(), SourcePinRequest{Directory: fixture.dir, Sources: []string{"umbrella"}, Bump: true, RejectRepublishedVersions: true})
	if err != nil {
		t.Fatal(err)
	}
	lock := fixture.lock(t)
	if lock.Artifacts["tool"].DerivedFrom != "umbrella@"+lock.Pins["umbrella"].Commit || bytes.Equal(before, readPinFixtureLock(t, fixture.dir)) {
		t.Fatal("unchanged members did not retain new umbrella provenance")
	}
}

func TestMaintenanceRejectsRepublishedImageParent(t *testing.T) {
	before := ArtifactPin{Tag: "v1.0.0", ImageParentDigest: "sha256:" + strings.Repeat("a", 64),
		Digests: map[string]string{"amd64": "sha256:" + strings.Repeat("b", 64)}}
	for _, test := range []struct {
		name, tag, parent string
		code              e.Code
	}{
		{"unchanged", "v1.0.0", before.ImageParentDigest, ""},
		{"changed same version", "v1.0.0", "sha256:" + strings.Repeat("c", 64), e.CodeUnverifiedRef},
		{"changed version alias", "1.0.0", "sha256:" + strings.Repeat("c", 64), e.CodeUnverifiedRef},
		{"removed parent", "v1.0.0", "", e.CodeUnverifiedRef},
		{"new version", "v1.1.0", "sha256:" + strings.Repeat("c", 64), ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			after := before
			after.Tag, after.ImageParentDigest = test.tag, test.parent
			after.DerivedFrom = "new-umbrella-provenance"
			err := validateUnchangedVersionBytes(SourcePinUpdate{Artifacts: []ArtifactPinChange{{Name: "image", Before: before, After: after}}})
			if test.code != "" {
				assertCatalogContractCode(t, err, test.code)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
