// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

func TestPublicationExecutableMembersUseRetainedBytes(t *testing.T) {
	root, digest := releasePublicationFixture(t)
	publication, err := OpenPublication(t.Context(), root, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer publication.Close()
	copyOf := *publication
	want, err := publication.ExecutableBundle("tools")
	if err != nil || len(want.Entries) != 1 || want.Entries[0].SHA256 != digestBytes([]byte("exact executable")) {
		t.Fatalf("bundle = %+v, %v", want, err)
	}
	changed, err := publication.ExecutableBundle("tools")
	if err != nil {
		t.Fatal(err)
	}
	changed.OS = "other"
	changed.Entries[0].Name = "substitution"
	changed.Entries[0].SHA256 = digestBytes([]byte("substitution"))
	changed.Entries[0].Size = 0
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		return os.WriteFile(path, []byte("substituted source bytes"), 0o600)
	}); err != nil {
		t.Fatal(err)
	}
	for _, handle := range []*Publication{publication, &copyOf} {
		got, err := handle.ExecutableBundle("tools")
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("retained metadata = %+v, %v", got, err)
		}
		var output bytes.Buffer
		written, err := handle.WriteExecutableMember(t.Context(), "tools", "cli", &output)
		if err != nil || output.String() != "exact executable" || written != int64(output.Len()) {
			t.Fatalf("retained member = %q, %d, %v", output.String(), written, err)
		}
	}
}

func TestPublicationCopiesShareRevocationAcrossMethods(t *testing.T) {
	for _, closeCopy := range []bool{false, true} {
		t.Run(map[bool]string{false: "close original", true: "close copy"}[closeCopy], func(t *testing.T) {
			root, digest := releasePublicationFixture(t)
			publication, err := OpenPublication(t.Context(), root, digest)
			if err != nil {
				t.Fatal(err)
			}
			copyOf := *publication
			toClose := publication
			if closeCopy {
				toClose = &copyOf
			}
			files := append([]*heldfile.File(nil), publication.files...)
			if err := toClose.Close(); err != nil {
				t.Fatal(err)
			}
			for _, file := range files {
				if file.OSFile() != nil {
					t.Fatal("publication copy leaked held file")
				}
			}
			for _, handle := range []*Publication{publication, &copyOf, nil, {}} {
				_, _, err := handle.Artifacts("registry.example.test/release@" + digest)
				assertRegistryCode(t, err, e.CodeGateUnmet)
				_, err = handle.Description("registry.example.test/release@" + digest)
				assertRegistryCode(t, err, e.CodeGateUnmet)
				assertRegistryCode(t, handle.RequireAttestedEvidence(), e.CodeGateUnmet)
				assertRegistryCode(t, handle.VisitEvidence(t.Context(), func(context.Context, EvidenceBinding, []byte) error {
					t.Error("closed publication visited evidence")
					return nil
				}), e.CodeGateUnmet)
				_, err = handle.WriteDiskPayload(t.Context(), "tools", io.Discard)
				assertRegistryCode(t, err, e.CodeGateUnmet)
				_, err = handle.ExecutableBundle("tools")
				assertRegistryCode(t, err, e.CodeGateUnmet)
				_, err = handle.WriteExecutableMember(t.Context(), "tools", "cli", io.Discard)
				assertRegistryCode(t, err, e.CodeGateUnmet)
				if err := handle.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestPublicationExecutableMemberAdmission(t *testing.T) {
	root, digest := releasePublicationFixture(t)
	publication, err := OpenPublication(t.Context(), root, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer publication.Close()
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name, output, member string
		ctx                  context.Context
		nilWriter            bool
		code                 e.Code
	}{
		{name: "nil context", output: "tools", member: "cli", code: e.CodeUsage},
		{name: "canceled", output: "tools", member: "cli", ctx: canceled, code: e.CodeTimeout},
		{name: "nil destination", output: "tools", member: "cli", ctx: t.Context(), nilWriter: true, code: e.CodeUsage},
		{name: "no output", member: "cli", ctx: t.Context(), code: e.CodeVerifyFailed},
		{name: "wrong output", output: "other", member: "cli", ctx: t.Context(), code: e.CodeVerifyFailed},
		{name: "output path", output: "./tools", member: "cli", ctx: t.Context(), code: e.CodeVerifyFailed},
		{name: "no member", output: "tools", ctx: t.Context(), code: e.CodeVerifyFailed},
		{name: "case differs", output: "tools", member: "CLI", ctx: t.Context(), code: e.CodeVerifyFailed},
		{name: "install path", output: "tools", member: "/usr/local/bin/cli", ctx: t.Context(), code: e.CodeVerifyFailed},
		{name: "archive path", output: "tools", member: "files/cli", ctx: t.Context(), code: e.CodeVerifyFailed},
		{name: "parent path", output: "tools", member: "../cli", ctx: t.Context(), code: e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			var target bytes.Buffer
			var destination io.Writer = &target
			if test.nilWriter {
				destination = nil
			}
			written, err := publication.WriteExecutableMember(test.ctx, test.output, test.member, destination)
			assertRegistryCode(t, err, test.code)
			if written != 0 || target.Len() != 0 {
				t.Fatal("refused stream produced bytes")
			}
		})
	}
}

type executableTestWriter func([]byte) (int, error)

func (writer executableTestWriter) Write(raw []byte) (int, error) { return writer(raw) }

func TestPublicationExecutableStreamFailures(t *testing.T) {
	root, digest := releasePublicationFixture(t)
	publication, err := OpenPublication(t.Context(), root, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer publication.Close()
	writeFailure := errors.New("destination failed")
	for _, test := range []struct {
		name string
		code e.Code
	}{
		{"write error", e.CodeVerifyFailed},
		{"short write", e.CodeVerifyFailed},
		{"cancel during write", e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			writer := executableTestWriter(func(raw []byte) (int, error) {
				switch test.name {
				case "write error":
					return 0, writeFailure
				case "short write":
					return len(raw) - 1, nil
				default:
					cancel()
					return len(raw), nil
				}
			})
			_, err := publication.WriteExecutableMember(ctx, "tools", "cli", writer)
			assertRegistryCode(t, err, test.code)
			if test.name == "write error" && !errors.Is(err, writeFailure) {
				t.Fatal("stream discarded writer cause")
			}
		})
	}
}

func TestPublicationCloseJoinsExecutableStream(t *testing.T) {
	root, digest := releasePublicationFixture(t)
	publication, err := OpenPublication(t.Context(), root, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer publication.Close()
	copyOf := *publication
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	streamDone, closeDone := make(chan error, 1), make(chan error, 1)
	go func() {
		_, err := publication.WriteExecutableMember(ctx, "tools", "cli", executableTestWriter(func(raw []byte) (int, error) {
			close(entered)
			<-release
			return len(raw), nil
		}))
		streamDone <- err
	}()
	<-entered
	go func() { closeDone <- copyOf.Close() }()
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned before active stream: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	close(release)
	assertRegistryCode(t, <-streamDone, e.CodeTimeout)
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	_, err = publication.ExecutableBundle("tools")
	assertRegistryCode(t, err, e.CodeGateUnmet)
}

func executableLayerFixture(t *testing.T, headers []tar.Header, payload string) *heldfile.File {
	t.Helper()
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	writer := tar.NewWriter(gz)
	for _, header := range headers {
		if err := writer.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if header.Size != 0 {
			if _, err := io.CopyN(writer, strings.NewReader(payload), header.Size); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := errors.Join(writer.Close(), gz.Close()); err != nil {
		t.Fatal(err)
	}
	held, err := heldfile.FromBytesContext(t.Context(), raw.Bytes(), heldfile.Options{Label: "executable-test-layer", MaxBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	return held
}

func TestExecutableMemberStreamUsesCompleteArchiveGrammar(t *testing.T) {
	const payload = "exact executable"
	valid := tar.Header{Name: "files/cli", Mode: 0o755, Typeflag: tar.TypeReg, Size: int64(len(payload))}
	for _, test := range []struct {
		name    string
		headers []tar.Header
		code    e.Code
	}{
		{"missing", nil, e.CodeReleaseIncomplete},
		{"extra after selected", []tar.Header{valid, {Name: "files/other", Mode: 0o755, Typeflag: tar.TypeReg}}, e.CodeVerifyFailed},
		{"duplicate", []tar.Header{valid, valid}, e.CodeVerifyFailed},
		{"traversal", []tar.Header{{Name: "files/../cli", Mode: 0o755, Typeflag: tar.TypeReg}}, e.CodeVerifyFailed},
		{"symlink", []tar.Header{{Name: "files/cli", Mode: 0o755, Typeflag: tar.TypeSymlink, Linkname: "../outside"}}, e.CodeVerifyFailed},
		{"hardlink", []tar.Header{{Name: "files/cli", Mode: 0o755, Typeflag: tar.TypeLink, Linkname: "other"}}, e.CodeVerifyFailed},
		{"mode overflow", []tar.Header{{Name: "files/cli", Mode: 0o755 + 1<<32, Typeflag: tar.TypeReg, Size: int64(len(payload))}}, e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			layer := executableLayerFixture(t, test.headers, payload)
			bundle := BinaryBundle{Entries: []BinaryEntry{{Name: "cli", Mode: 0o755, Size: int64(len(payload)), SHA256: digestBytes([]byte(payload))}}}
			publication := &Publication{publicationState: &publicationState{executables: map[string]publicationExecutable{"tools": {bundle: bundle, layer: layer}}}}
			_, err := publication.WriteExecutableMember(t.Context(), "tools", "cli", io.Discard)
			assertRegistryCode(t, err, test.code)
			assertRegistryCode(t, extractBundleLayer(t.Context(), layer, t.TempDir(), bundle), test.code)
		})
	}
}

func TestExecutableMemberStreamAdmitsModeAndBounds(t *testing.T) {
	for _, test := range []struct {
		name string
		mode uint32
		size int64
		code e.Code
	}{
		{"not executable", 0o644, 0, e.CodeEngineCapability},
		{"oversized", 0o755, maxDeliveryFileBytes + 1, e.CodeEngineCapability},
		{"negative size", 0o755, -1, e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			layer := executableLayerFixture(t, nil, "")
			bundle := BinaryBundle{Entries: []BinaryEntry{{Name: "cli", Mode: test.mode, Size: test.size, SHA256: digestBytes(nil)}}}
			publication := &Publication{publicationState: &publicationState{executables: map[string]publicationExecutable{"tools": {bundle: bundle, layer: layer}}}}
			var destination bytes.Buffer
			written, err := publication.WriteExecutableMember(t.Context(), "tools", "cli", &destination)
			assertRegistryCode(t, err, test.code)
			if written != 0 || destination.Len() != 0 {
				t.Fatal("unsupported member produced bytes")
			}
		})
	}
}

func TestExecutableMemberStreamChecksUnselectedMembers(t *testing.T) {
	const payload = "exact tool bytes"
	headers := []tar.Header{
		{Name: "files/first", Mode: 0o755, Typeflag: tar.TypeReg, Size: int64(len(payload))},
		{Name: "files/second", Mode: 0o755, Typeflag: tar.TypeReg, Size: int64(len(payload))},
	}
	layer := executableLayerFixture(t, headers, payload)
	for _, test := range []struct {
		name, member, wrongDigest string
	}{
		{name: "first member", member: "first"},
		{name: "second member", member: "second"},
		{name: "corrupt earlier sibling", member: "second", wrongDigest: "first"},
		{name: "corrupt later sibling", member: "first", wrongDigest: "second"},
	} {
		t.Run(test.name, func(t *testing.T) {
			bundle := BinaryBundle{}
			for _, header := range headers {
				name := strings.TrimPrefix(header.Name, "files/")
				entry := BinaryEntry{Name: name, Mode: 0o755, Size: int64(len(payload)), SHA256: digestBytes([]byte(payload))}
				if name == test.wrongDigest {
					entry.SHA256 = digestBytes([]byte("different bytes"))
				}
				bundle.Entries = append(bundle.Entries, entry)
			}
			publication := &Publication{publicationState: &publicationState{executables: map[string]publicationExecutable{"tools": {bundle: bundle, layer: layer}}}}
			var destination bytes.Buffer
			written, err := publication.WriteExecutableMember(t.Context(), "tools", test.member, &destination)
			if test.wrongDigest != "" {
				assertRegistryCode(t, err, e.CodeVerifyFailed)
				return
			}
			if err != nil || destination.String() != payload || written != int64(len(payload)) {
				t.Fatalf("selected member = %q, %d, %v", destination.String(), written, err)
			}
		})
	}
}
