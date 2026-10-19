// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package releaseasset

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type zipEntry struct {
	name string
	mode fs.FileMode
	body string
}

func zipRelease(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()
	var carrier bytes.Buffer
	writer := zip.NewWriter(&carrier)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Store}
		header.SetMode(entry.mode)
		member, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(member, entry.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return carrier.Bytes()
}

func TestSelectZip(t *testing.T) {
	tool := zipEntry{"bin/tool", 0755, "xxxx"}
	valid := zipRelease(t, zipEntry{"bin/", fs.ModeDir | 0755, ""}, tool)
	corrupt := bytes.Clone(valid)
	corrupt[bytes.Index(corrupt, []byte("xxxx"))] ^= 1
	remainder := zipRelease(t, tool, zipEntry{"LICENSE", 0644, "license"})
	remainder[bytes.Index(remainder, []byte("license"))] ^= 1
	for _, test := range []struct {
		name   string
		data   []byte
		limits Limits
		code   e.Code
	}{
		{name: "regular", data: valid},
		{name: "relative spelling", data: zipRelease(t, zipEntry{"./bin/tool", 0755, "xxxx"})},
		{name: "empty member", data: zipRelease(t, zipEntry{"bin/tool", 0755, ""}), code: e.CodeVerifyFailed},
		{name: "missing member", data: zipRelease(t, zipEntry{"other", 0755, "xxxx"}), code: e.CodeVerifyFailed},
		{name: "duplicate", data: zipRelease(t, tool, tool), code: e.CodeVerifyFailed},
		{name: "alias duplicate", data: zipRelease(t, tool, zipEntry{"./bin/tool", 0755, "xxxx"}), code: e.CodeVerifyFailed},
		{name: "traversal", data: zipRelease(t, tool, zipEntry{"../escape", 0755, ""}), code: e.CodeVerifyFailed},
		{name: "absolute", data: zipRelease(t, tool, zipEntry{"/escape", 0755, ""}), code: e.CodeVerifyFailed},
		{name: "symlink", data: zipRelease(t, tool, zipEntry{"link", fs.ModeSymlink | 0777, "bin/tool"}), code: e.CodeVerifyFailed},
		{name: "device", data: zipRelease(t, tool, zipEntry{"device", fs.ModeDevice | 0600, ""}), code: e.CodeVerifyFailed},
		{name: "selected directory", data: zipRelease(t, zipEntry{"bin/tool/", fs.ModeDir | 0755, ""}), code: e.CodeVerifyFailed},
		{name: "CRC", data: corrupt, code: e.CodeVerifyFailed},
		{name: "remaining member CRC", data: remainder, code: e.CodeVerifyFailed},
		{name: "truncated", data: valid[:len(valid)-8], code: e.CodeVerifyFailed},
		{name: "executable limit", data: valid, limits: Limits{ExecutableBytes: 3, ExpandedBytes: 16, Entries: 8}, code: e.CodeVerifyFailed},
		{name: "expanded limit", data: zipRelease(t, tool, zipEntry{"LICENSE", 0644, "license"}), limits: Limits{ExecutableBytes: 4, ExpandedBytes: 8, Entries: 8}, code: e.CodeVerifyFailed},
		{name: "entry limit", data: valid, limits: Limits{ExecutableBytes: 8, ExpandedBytes: 16, Entries: 1}, code: e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			limits := test.limits
			if limits == (Limits{}) {
				limits = Limits{ExecutableBytes: 8, ExpandedBytes: 32, Entries: 8}
			}
			got, err := Select(t.Context(), bytes.NewReader(test.data), int64(len(test.data)), "zip", "bin/tool", limits)
			if test.code != "" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != test.code || got != nil {
					t.Fatalf("result %q, error %v; want %s and no output", got, err, test.code)
				}
			} else if err != nil || string(got) != "xxxx" {
				t.Fatalf("result %q, error %v", got, err)
			}
		})
	}
}

func TestSelectArchiveAdmission(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name           string
		ctx            context.Context
		format, member string
		size           int64
		limits         Limits
		code           e.Code
	}{
		{name: "nil context", code: e.CodeUsage},
		{name: "canceled", ctx: canceled, code: e.CodeTimeout},
		{name: "empty carrier", ctx: t.Context(), code: e.CodeVerifyFailed},
		{name: "oversized carrier", ctx: t.Context(), size: MaxToolTransportBytes + 1, code: e.CodeVerifyFailed},
		{name: "unknown format", ctx: t.Context(), size: 1, format: "7z", code: e.CodeInvalidCatalog},
		{name: "unsafe member", ctx: t.Context(), size: 1, format: "zip", member: "../tool", limits: ToolLimits(), code: e.CodeInvalidCatalog},
		{name: "unbounded", ctx: t.Context(), size: 1, format: "zip", member: "tool", code: e.CodeInvalidCatalog},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := Select(test.ctx, bytes.NewReader(nil), test.size, test.format, test.member, test.limits)
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != test.code || got != nil {
				t.Fatalf("result %q, error %v; want %s and no output", got, err, test.code)
			}
		})
	}
}
