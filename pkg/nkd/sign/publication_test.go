// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

func qualifiedPublicationFixture(t *testing.T, document []byte) SignedDocument {
	t.Helper()
	signed, err := signedDocumentFromExactOutputs(document, []byte("detached-signature"),
		testSigningCertificate(t, "publisher@example.test", "https://issuer.example.test"), nil)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func TestSignedDocumentPublishCommitsExactImmutableSet(t *testing.T) {
	document := []byte(`{"schemaVersion":"test/v1"}`)
	signed := qualifiedPublicationFixture(t, document)
	out := filepath.Join(t.TempDir(), "document.json")
	if err := signed.Publish(context.Background(), PublicationRequest{
		Document: document, DocumentPath: out,
	}); err != nil {
		t.Fatal(err)
	}
	certificate, _ := signed.Certificate()
	for path, want := range map[string][]byte{
		out: document, out + ".sig": []byte("detached-signature"), out + ".pem": certificate,
	} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(want) {
			t.Fatalf("published %s = %q, want %q: %v", path, got, want, err)
		}
	}
	if err := signed.Publish(context.Background(), PublicationRequest{
		Document: document, DocumentPath: out,
	}); signCode(err) != nkderr.CodeImmutableViolation {
		t.Fatalf("immutable republish code = %q, want %q: %v",
			signCode(err), nkderr.CodeImmutableViolation, err)
	}
}

func TestSignedDocumentPublishRollsBackCommitFailure(t *testing.T) {
	document := []byte(`{"schemaVersion":"test/v1"}`)
	signed := qualifiedPublicationFixture(t, document)
	out := filepath.Join(t.TempDir(), "document.json")
	injected := errors.New("injected commit failure")
	err := signed.publish(context.Background(), PublicationRequest{
		Document: document, DocumentPath: out,
	}, func(committed int, _ string) error {
		if committed == 1 {
			return injected
		}
		return nil
	})
	if signCode(err) != nkderr.CodeInternal || !errors.Is(err, injected) {
		t.Fatalf("injected failure was not retained as a coded error: %v", err)
	}
	assertPublicationAbsent(t, out)
}

func TestSignedDocumentPublishRollsBackCancellationBetweenMembers(t *testing.T) {
	document := []byte(`{"schemaVersion":"test/v1"}`)
	signed := qualifiedPublicationFixture(t, document)
	out := filepath.Join(t.TempDir(), "document.json")
	ctx, cancel := context.WithCancel(context.Background())
	err := signed.publish(ctx, PublicationRequest{
		Document: document, DocumentPath: out,
	}, func(committed int, _ string) error {
		if committed == 1 {
			cancel()
		}
		return nil
	})
	if signCode(err) != nkderr.CodeTimeout {
		t.Fatalf("canceled publication code = %q, want %q: %v",
			signCode(err), nkderr.CodeTimeout, err)
	}
	assertPublicationAbsent(t, out)
}

func TestSignedDocumentPublishRejectsCanceledContextBeforeFilesystemWork(t *testing.T) {
	document := []byte(`{"schemaVersion":"test/v1"}`)
	signed := qualifiedPublicationFixture(t, document)
	parent := t.TempDir()
	out := filepath.Join(parent, "document.json")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := signed.Publish(ctx, PublicationRequest{Document: document, DocumentPath: out})
	if signCode(err) != nkderr.CodeTimeout {
		t.Fatalf("canceled publication code = %q, want %q: %v",
			signCode(err), nkderr.CodeTimeout, err)
	}
	entries, readErr := os.ReadDir(parent)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("canceled publication touched the output directory: %v", entries)
	}
}

func TestSignedDocumentPublishNeverOverwritesPreexistingMember(t *testing.T) {
	document := []byte(`{"schemaVersion":"test/v1"}`)
	signed := qualifiedPublicationFixture(t, document)
	out := filepath.Join(t.TempDir(), "document.json")
	if err := os.WriteFile(out+".sig", []byte("operator-owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := signed.Publish(context.Background(), PublicationRequest{
		Document: document, DocumentPath: out,
	})
	if signCode(err) != nkderr.CodeImmutableViolation {
		t.Fatalf("preexisting member code = %q, want %q: %v",
			signCode(err), nkderr.CodeImmutableViolation, err)
	}
	got, readErr := os.ReadFile(out + ".sig")
	if readErr != nil || string(got) != "operator-owned" {
		t.Fatalf("preexisting signature was changed: %q (%v)", got, readErr)
	}
	for _, path := range []string{out, out + ".pem"} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("failed preflight created %s: %v", path, statErr)
		}
	}
}

func TestSignedDocumentPublishConcurrentCommitHasOneCompleteWinner(t *testing.T) {
	document := []byte(`{"schemaVersion":"test/v1"}`)
	signed := qualifiedPublicationFixture(t, document)
	out := filepath.Join(t.TempDir(), "document.json")
	start := make(chan struct{})
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = signed.Publish(context.Background(), PublicationRequest{
				Document: document, DocumentPath: out,
			})
		}(i)
	}
	close(start)
	wg.Wait()
	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
			continue
		}
		if code := signCode(err); code != nkderr.CodeImmutableViolation && code != nkderr.CodeWriteConflict {
			t.Fatalf("losing publication returned %q: %v", code, err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful publishers = %d, want 1 (errors=%v)", successes, errs)
	}
	for _, path := range []string{out, out + ".sig", out + ".pem"} {
		if _, err := os.ReadFile(path); err != nil {
			t.Fatalf("winning publication is incomplete at %s: %v", path, err)
		}
	}
}

func TestSignedDocumentPublishRecoversAfterProcessCrash(t *testing.T) {
	const childFlag = "NKD_SIGN_PUBLICATION_CRASH_CHILD"
	if os.Getenv(childFlag) == "1" {
		document := []byte(`{"schemaVersion":"test/v1"}`)
		certificate, err := os.ReadFile(os.Getenv("NKD_SIGN_PUBLICATION_CERT"))
		if err != nil {
			os.Exit(92)
		}
		signed, err := signedDocumentFromExactOutputs(document,
			[]byte("detached-signature"), certificate, nil)
		if err != nil {
			os.Exit(93)
		}
		_ = signed.publish(context.Background(), PublicationRequest{
			Document: document, DocumentPath: os.Getenv("NKD_SIGN_PUBLICATION_OUT"),
		}, func(committed int, _ string) error {
			if committed == 1 {
				os.Exit(91)
			}
			return nil
		})
		os.Exit(94)
	}

	document := []byte(`{"schemaVersion":"test/v1"}`)
	dir := t.TempDir()
	out := filepath.Join(dir, "document.json")
	certificate := testSigningCertificate(t, "publisher@example.test", "https://issuer.example.test")
	certificatePath := filepath.Join(dir, "fixture.pem")
	if err := os.WriteFile(certificatePath, certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(self, "-test.run=^TestSignedDocumentPublishRecoversAfterProcessCrash$")
	command.Env = append(os.Environ(),
		childFlag+"=1",
		"NKD_SIGN_PUBLICATION_CERT="+certificatePath,
		"NKD_SIGN_PUBLICATION_OUT="+out,
	)
	err = command.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 91 {
		t.Fatalf("crash child = %v, want exit 91", err)
	}
	if _, err := os.Lstat(out + ".publish.pending.json"); err != nil {
		t.Fatalf("crash did not retain a recovery journal: %v", err)
	}
	conflictingDocument := []byte(`{"schemaVersion":"test/v2"}`)
	if err := PreparePublication(context.Background(), PublicationRequest{
		Document: conflictingDocument, DocumentPath: out,
	}); signCode(err) != nkderr.CodeWriteConflict {
		t.Fatalf("conflicting recovery code = %q, want %q: %v",
			signCode(err), nkderr.CodeWriteConflict, err)
	}
	if _, err := os.Lstat(out + ".publish.pending.json"); err != nil {
		t.Fatalf("conflicting recovery removed the pending transaction: %v", err)
	}
	if err := PreparePublication(context.Background(), PublicationRequest{
		Document: document, DocumentPath: out,
	}); err != nil {
		t.Fatalf("pre-sign recovery did not reclaim the interrupted transaction: %v", err)
	}
	assertPublicationAbsent(t, out)

	signed, err := signedDocumentFromExactOutputs(document,
		[]byte("fresh-detached-signature"), certificate, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := signed.Publish(context.Background(), PublicationRequest{
		Document: document, DocumentPath: out,
	}); err != nil {
		t.Fatalf("retry did not recover and republish the exact set: %v", err)
	}
	for _, path := range []string{out, out + ".sig", out + ".pem"} {
		if _, err := os.ReadFile(path); err != nil {
			t.Fatalf("recovered publication is incomplete at %s: %v", path, err)
		}
	}
	if got, err := os.ReadFile(out + ".sig"); err != nil || string(got) != "fresh-detached-signature" {
		t.Fatalf("recovered publication signature = %q, want fresh sidecar: %v", got, err)
	}
	if _, err := os.Lstat(out + ".publish.pending.json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("successful recovery retained pending journal: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, ".nkd-signed-*.tmp"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("successful recovery retained stages %v: %v", matches, err)
	}
}

func assertPublicationAbsent(t *testing.T, out string) {
	t.Helper()
	for _, path := range []string{out, out + ".sig", out + ".pem"} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed publication left %s: %v", path, err)
		}
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(out), ".nkd-signed-*.tmp"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("publication left staging files %v: %v", matches, err)
	}
}
