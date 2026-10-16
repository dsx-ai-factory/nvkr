// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package cistore

import (
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func validLocator() Locator {
	return Locator{SchemaVersion: SchemaVersion, Request: testRequest, Reference: testRequest.packageName() + "@sha256:" + strings.Repeat("a", 64), ArchiveDigest: "sha256:" + strings.Repeat("b", 64), ArchiveSize: 1024, Paths: []string{"data/a", "data/b"}, StripPrefix: "data"}
}

func TestDecodeLocatorStrictMetadata(t *testing.T) {
	l := validLocator()
	raw, err := canonical.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		raw  []byte
	}{
		{"empty", nil},
		{"oversized", []byte(strings.Repeat(" ", MaxLocatorBytes+1))},
		{"whitespace", append(append([]byte{}, raw...), '\n')},
		{"duplicate key", []byte(strings.Replace(string(raw), `"archiveSize":1024`, `"archiveSize":1024,"archiveSize":1024`, 1))},
		{"unknown key", []byte(strings.TrimSuffix(string(raw), "}") + `,"unknown":true}`)},
		{"trailing value", append(append([]byte{}, raw...), []byte("{}")...)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeLocator(tc.raw)
			assertCode(t, err, e.CodeVerifyFailed)
		})
	}
	projected, err := l.ProjectedPaths()
	if err != nil || strings.Join(projected, ",") != "a,b" {
		t.Fatalf("unexpected projected roots: %v %v", projected, err)
	}
}

func TestLocatorRejectsInvalidShape(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Locator)
	}{
		{"overlapping nonadjacent", func(l *Locator) { l.Paths = []string{"data/a", "data/a-b", "data/a/x"} }},
		{"duplicate roots", func(l *Locator) { l.Paths = []string{"data/a", "data/a"} }},
		{"unsorted roots", func(l *Locator) { l.Paths = []string{"data/b", "data/a"} }},
		{"unclean roots", func(l *Locator) { l.Paths = []string{"data/./a"} }},
		{"absolute roots", func(l *Locator) { l.Paths = []string{"/data/a"} }},
		{"not ancestor", func(l *Locator) { l.StripPrefix = "other" }},
		{"oversized archive", func(l *Locator) { l.ArchiveSize = 1 << 40 }},
		{"empty archive", func(l *Locator) { l.ArchiveSize = 0 }},
		{"wrong schema", func(l *Locator) { l.SchemaVersion = "other" }},
		{"wrong hash", func(l *Locator) { l.ArchiveDigest = "sha512:" + strings.Repeat("0", 128) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := validLocator()
			tc.edit(&l)
			assertCode(t, l.Validate(testRequest), e.CodeVerifyFailed)
		})
	}
}

func TestRequestAndCredentialAdmission(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*Request)
	}{
		{"query", func(r *Request) { r.Repository += "?token=secret" }},
		{"encoded path", func(r *Request) { r.Repository = "Example/Source%2Fother" }},
		{"extra segment", func(r *Request) { r.Repository += "/other" }},
		{"dot owner", func(r *Request) { r.Repository = "./Source" }},
		{"parent owner", func(r *Request) { r.Repository = "../invalid" }},
		{"dot repository", func(r *Request) { r.Repository = "Example/." }},
		{"parent repository", func(r *Request) { r.Repository = "Example/.." }},
		{"noncanonical run", func(r *Request) { r.RunID = "00123" }},
		{"zero attempt", func(r *Request) { r.Attempt = "0" }},
		{"negative attempt", func(r *Request) { r.Attempt = "-1" }},
		{"unsafe name", func(r *Request) { r.Name = "../payload" }},
		{"control name", func(r *Request) { r.Name = "payload\n" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := testRequest
			tc.edit(&r)
			_, _, _, err := admit(t.Context(), r, testOptions)
			assertCode(t, err, e.CodeUsage)
		})
	}
	_, _, _, err := admit(t.Context(), testRequest, Options{})
	assertCode(t, err, e.CodeTrustPolicyMissing)
	_, _, _, err = admit(nil, testRequest, testOptions) //nolint:staticcheck // Exercise the public nil-context admission boundary.
	assertCode(t, err, e.CodeUsage)
}
