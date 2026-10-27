// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package transportarchive

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const nestedMember = ".nvkr/carriers/payload.tar"

func nestedFixture(t *testing.T, member string, extra bool) []byte {
	t.Helper()
	inner := streamFixture(t)
	var raw bytes.Buffer
	writer := tar.NewWriter(&raw)
	if err := writer.WriteHeader(&tar.Header{Name: member, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(inner))}); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(inner); err != nil {
		t.Fatal(err)
	}
	if extra {
		if err := writer.WriteHeader(&tar.Header{Name: "extra", Typeflag: tar.TypeReg, Mode: 0o644}); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return raw.Bytes()
}

func TestNestedStreamAuthenticatesWholeOuterBeforeInnerPublication(t *testing.T) {
	for _, test := range []struct {
		name, member string
		trailing     string
		extra        bool
		badDigest    bool
		sizeChange   int64
		lateError    bool
		wrongInner   bool
		want         e.Code
	}{
		{name: "exact", member: nestedMember},
		{name: "authenticated outer trailer", member: nestedMember, trailing: "outer padding"},
		{name: "changed outer trailer", member: nestedMember, trailing: "outer padding", badDigest: true, want: e.CodeVerifyFailed},
		{name: "wrong outer member", member: "other.tar", want: e.CodeVerifyFailed},
		{name: "additional outer member", member: nestedMember, extra: true, want: e.CodeVerifyFailed},
		{name: "wrong outer digest", member: nestedMember, badDigest: true, want: e.CodeVerifyFailed},
		{name: "short outer stream", member: nestedMember, sizeChange: 1, want: e.CodeVerifyFailed},
		{name: "extra outer byte", member: nestedMember, sizeChange: -1, want: e.CodeVerifyFailed},
		{name: "late chunk failure", member: nestedMember, lateError: true, want: e.CodeVerifyFailed},
		{name: "unselected inner root", member: nestedMember, wrongInner: true, want: e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := append(nestedFixture(t, test.member, test.extra), test.trailing...)
			digest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
			if test.badDigest {
				digest = "sha256:" + strings.Repeat("0", 64)
			}
			root := t.TempDir()
			checked := false
			reader := checkedEOFReader{Reader: bytes.NewReader(raw), check: func() {
				checked = true
				if _, err := os.Lstat(filepath.Join(root, "payload")); !os.IsNotExist(err) {
					t.Fatal("inner root published before outer source EOF")
				}
				if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(nestedMember))); !os.IsNotExist(err) {
					t.Fatal("nested carrier was materialized")
				}
			}}
			if test.lateError {
				reader.err = io.ErrUnexpectedEOF
			}
			allowed := []string{"payload"}
			if test.wrongInner {
				allowed = []string{"other"}
			}
			err := UnpackNestedStream(t.Context(), reader, int64(len(raw))+test.sizeChange, digest, nestedMember, root, allowed)
			if test.want != "" {
				if transportCode(err) != test.want {
					t.Fatalf("nested restore = %v, want %s", err, test.want)
				}
				if test.lateError && !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatal("nested verifier hid the typed source failure")
				}
				entries, readErr := os.ReadDir(root)
				if readErr != nil || len(entries) != 0 {
					t.Fatal("failed nested restore retained extracted roots")
				}
				return
			}
			if err != nil || !checked {
				t.Fatalf("nested source did not reach verified EOF: %v", err)
			}
			got, err := os.ReadFile(filepath.Join(root, "payload"))
			if err != nil || string(got) != "content" {
				t.Fatalf("nested payload differs: %v", err)
			}
		})
	}
}

func TestNestedStreamRejectsUnsafeSelectionBeforeReading(t *testing.T) {
	for _, member := range []string{"", "../escape", "/absolute", "a/../payload", `a\payload`} {
		t.Run(member, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "absent")
			err := UnpackNestedStream(t.Context(), unreadNestedSource{t}, 512,
				"sha256:"+strings.Repeat("a", 64), member, root, []string{"payload"})
			if transportCode(err) != e.CodeUsage {
				t.Fatalf("unsafe member = %v", err)
			}
			if _, err := os.Lstat(root); !os.IsNotExist(err) {
				t.Fatal("unsafe member caused filesystem work")
			}
		})
	}
}

type unreadNestedSource struct{ t *testing.T }

func (r unreadNestedSource) Read([]byte) (int, error) {
	r.t.Fatal("invalid nested selection reached source I/O")
	return 0, io.EOF
}
