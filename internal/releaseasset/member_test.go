// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package releaseasset

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func releaseArchive(t *testing.T, headers ...tar.Header) []byte {
	t.Helper()
	var result bytes.Buffer
	gz := gzip.NewWriter(&result)
	tw := tar.NewWriter(gz)
	for _, header := range headers {
		if err := tw.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := tw.Write(bytes.Repeat([]byte("x"), int(header.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return result.Bytes()
}

func TestSelectTarGzip(t *testing.T) {
	regular := tar.Header{Name: "bin/tool", Size: 4, Typeflag: tar.TypeReg, Mode: 0755}
	valid := releaseArchive(t, tar.Header{Name: "bin/", Typeflag: tar.TypeDir}, regular)
	corrupt := bytes.Clone(valid)
	corrupt[len(corrupt)-8] ^= 1
	for _, test := range []struct {
		name   string
		data   []byte
		limits Limits
		code   e.Code
	}{
		{name: "regular", data: valid},
		{name: "relative spelling", data: releaseArchive(t, tar.Header{Name: ".", Typeflag: tar.TypeDir}, tar.Header{Name: "./bin/", Typeflag: tar.TypeDir}, tar.Header{Name: "./bin/tool", Size: 4, Typeflag: tar.TypeReg})},
		{name: "relative alias duplicate", data: releaseArchive(t, regular, tar.Header{Name: "./bin/tool", Size: 4, Typeflag: tar.TypeReg}), code: e.CodeVerifyFailed},
		{name: "relative traversal", data: releaseArchive(t, regular, tar.Header{Name: "./../escape", Typeflag: tar.TypeReg}), code: e.CodeVerifyFailed},
		{name: "multiple dot prefixes", data: releaseArchive(t, regular, tar.Header{Name: "././escape", Typeflag: tar.TypeReg}), code: e.CodeVerifyFailed},
		{name: "duplicate root", data: releaseArchive(t, tar.Header{Name: ".", Typeflag: tar.TypeDir}, tar.Header{Name: "./", Typeflag: tar.TypeDir}, regular), code: e.CodeVerifyFailed},
		{name: "regular root", data: releaseArchive(t, tar.Header{Name: ".", Typeflag: tar.TypeReg}, regular), code: e.CodeVerifyFailed},
		{name: "missing", data: releaseArchive(t, tar.Header{Name: "other", Size: 1, Typeflag: tar.TypeReg}), code: e.CodeVerifyFailed},
		{name: "empty", data: releaseArchive(t, tar.Header{Name: "bin/tool", Typeflag: tar.TypeReg}), code: e.CodeVerifyFailed},
		{name: "duplicate", data: releaseArchive(t, regular, regular), code: e.CodeVerifyFailed},
		{name: "traversal", data: releaseArchive(t, regular, tar.Header{Name: "../escape", Typeflag: tar.TypeReg}), code: e.CodeVerifyFailed},
		{name: "symlink", data: releaseArchive(t, regular, tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "bin/tool"}), code: e.CodeVerifyFailed},
		{name: "hardlink", data: releaseArchive(t, tar.Header{Name: "bin/tool", Typeflag: tar.TypeLink, Linkname: "other"}), code: e.CodeVerifyFailed},
		{name: "checksum", data: corrupt, code: e.CodeVerifyFailed},
		{name: "truncated", data: valid[:len(valid)-4], code: e.CodeVerifyFailed},
		{name: "hidden archive", data: append(bytes.Clone(valid), valid...), code: e.CodeVerifyFailed},
		{name: "executable limit", data: valid, limits: Limits{ExecutableBytes: 3, ExpandedBytes: 8192, Entries: 8}, code: e.CodeVerifyFailed},
		{name: "expanded limit", data: valid, limits: Limits{ExecutableBytes: 4, ExpandedBytes: 100, Entries: 8}, code: e.CodeVerifyFailed},
		{name: "entry limit", data: valid, limits: Limits{ExecutableBytes: 8, ExpandedBytes: 8192, Entries: 1}, code: e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			limits := test.limits
			if limits == (Limits{}) {
				limits = Limits{ExecutableBytes: 8, ExpandedBytes: 8192, Entries: 8}
			}
			got, err := SelectTarGzip(t.Context(), bytes.NewReader(test.data), "bin/tool", limits)
			if test.code != "" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != test.code || got != nil {
					t.Fatalf("result %q, error %v; want %s and no output", got, err, test.code)
				}
				return
			}
			if err != nil || string(got) != "xxxx" {
				t.Fatalf("result %q, error %v", got, err)
			}
		})
	}
}

func TestSelectTarGzipAdmission(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name   string
		ctx    context.Context
		member string
		limits Limits
		code   e.Code
	}{
		{name: "nil context", member: "tool", code: e.CodeUsage},
		{name: "canceled", ctx: ctx, member: "tool", code: e.CodeTimeout},
		{name: "unsafe member", ctx: t.Context(), member: "../tool", limits: Limits{ExecutableBytes: 8, ExpandedBytes: 8192, Entries: 8}, code: e.CodeInvalidCatalog},
		{name: "unbounded", ctx: t.Context(), member: "tool", code: e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := SelectTarGzip(test.ctx, bytes.NewReader(nil), test.member, test.limits)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != test.code {
				t.Fatalf("error %v; want %s", err, test.code)
			}
		})
	}
}
