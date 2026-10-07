// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package profile

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	nkderr "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"golang.org/x/sys/unix"
	"sigs.k8s.io/yaml"
)

func code(err error) nkderr.Code {
	if e, ok := err.(*nkderr.Error); ok {
		return e.Code
	}
	return ""
}

// The profile digest is a deterministic, per-arch identity: stable across calls and
// distinct per target arch (byte reproduction is bounded to it).
func TestDigestDeterministicAndPerArch(t *testing.T) {
	a1, err := Embedded("amd64").Digest()
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := Embedded("amd64").Digest()
	if a1 != a2 {
		t.Fatalf("profile digest not deterministic: %s vs %s", a1, a2)
	}
	arm, _ := Embedded("arm64").Digest()
	if a1 == arm {
		t.Fatalf("amd64 and arm64 profile digests must differ (got %s for both)", a1)
	}
}

func TestMarshalIsExactCanonicalSigningPayload(t *testing.T) {
	p := Embedded("amd64")
	p.Provides = []string{"nix", "oci-hermetic"}
	// Deliberately scramble authoring order; the authority bytes must not inherit it.
	p.Fields[0], p.Fields[len(p.Fields)-1] = p.Fields[len(p.Fields)-1], p.Fields[0]

	got, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	want, err := canonical.Marshal(p.canonical())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("Marshal is not the exact canonical payload:\n got %q\nwant %q", got, want)
	}
	if len(got) == 0 || got[len(got)-1] == '\n' || bytes.Contains(got, []byte("\n")) {
		t.Fatalf("canonical payload contains display whitespace or trailing newline: %q", got)
	}
	if !json.Valid(got) {
		t.Fatalf("canonical profile is not JSON: %q", got)
	}

	var roundTrip BuilderProfile
	if err := json.Unmarshal(got, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if err := roundTrip.Validate(); err != nil {
		t.Fatalf("canonical profile did not validate after round trip: %v", err)
	}
	again, err := roundTrip.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, again) {
		t.Fatalf("canonical round trip changed exact bytes:\nfirst %q\nagain %q", got, again)
	}
}

func TestLoadAcceptsAuthoredYAML(t *testing.T) {
	p := Embedded("arm64")
	display, err := yaml.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(display, authority) || !bytes.Contains(display, []byte("schemaVersion:")) {
		t.Fatalf("display YAML was not clearly distinct from canonical authority JSON: %q", display)
	}
	path := filepath.Join(t.TempDir(), "authored-profile.yaml")
	if err := os.WriteFile(path, display, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadContext(context.Background(), path)
	if err != nil {
		t.Fatalf("Load rejected authored YAML: %v", err)
	}
	loadedAuthority, err := loaded.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(authority, loadedAuthority) {
		t.Fatalf("YAML load changed canonical authority bytes:\nwant %q\n got %q", authority, loadedAuthority)
	}
}

func TestLoadContextRejectsUnsafeOrOversizedProfilePaths(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		dir := t.TempDir()
		target := filepath.Join(dir, "target.yaml")
		link := filepath.Join(dir, "profile.yaml")
		raw, err := Embedded("amd64").Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadContext(context.Background(), link); code(err) != nkderr.CodeInvalidCatalog {
			t.Fatalf("error = %v, want %s", err, nkderr.CodeInvalidCatalog)
		}
	})

	t.Run("fifo", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "profile.pipe")
		if err := unix.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadContext(context.Background(), path); code(err) != nkderr.CodeInvalidCatalog {
			t.Fatalf("error = %v, want %s", err, nkderr.CodeInvalidCatalog)
		}
	})

	t.Run("oversized", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "profile.yaml")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(maxAuthoredBuilderProfileBytes + 1); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadContext(context.Background(), path); code(err) != nkderr.CodeInvalidCatalog {
			t.Fatalf("error = %v, want %s", err, nkderr.CodeInvalidCatalog)
		}
	})
}

func TestLoadContextRejectsCancellationBeforePathInspection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.pipe")
	if err := unix.Mkfifo(path, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := LoadContext(ctx, path); code(err) != nkderr.CodeTimeout || !nkderr.Retryable(err) {
		t.Fatalf("error = %v, want retryable %s before path inspection", err, nkderr.CodeTimeout)
	}
}

func TestMarshalRefusesInvalidAuthorityDocument(t *testing.T) {
	p := Embedded("amd64")
	p.Repin(FieldRunnerDigest, "sha256:not-exact-bytes", false)
	if _, err := p.Marshal(); code(err) != nkderr.CodeInvalidCatalog {
		t.Fatalf("authority output accepted a malformed execution identity: %v", err)
	}
}

// Preflight fails closed on a hard-fail divergence and passes (recording advisory
// divergences) otherwise — the NKD v1 §8.2.1 fail-closed host gate.
func TestPreflightHardFailClosed(t *testing.T) {
	p := Embedded("amd64") // target.arch=amd64 is hard-fail
	// Host is arm64 -> hard-fail divergence -> CodePreflightFailed.
	_, divs, err := p.VerifyHost(context.Background(), map[string]string{FieldTargetArch: "arm64", FieldRunnerOS: "linux"})
	if code(err) != nkderr.CodePreflightFailed {
		t.Fatalf("hard-fail divergence must fail closed, got %v", err)
	}
	if len(divs) == 0 {
		t.Fatal("divergences must be recorded for the evidence bundle")
	}
	// Matching arch, only an advisory (runner.os) divergence -> no error.
	_, _, err = p.VerifyHost(context.Background(), map[string]string{FieldTargetArch: "amd64", FieldRunnerOS: "darwin"})
	if err != nil {
		t.Fatalf("advisory-only divergence must not fail closed, got %v", err)
	}
	// Fully matching host -> no divergence, no error.
	_, divs, err = p.VerifyHost(context.Background(), map[string]string{
		FieldTargetArch: "amd64", FieldRunnerOS: "linux", FieldRuntimeCapability: "available",
		FieldKernel: "any", FieldClockPolicy: "SOURCE_DATE_EPOCH+TZ=UTC+LC_ALL=C",
	})
	if err != nil || len(divs) != 0 {
		t.Fatalf("fully matching host must pass with no divergences: err=%v divs=%v", err, divs)
	}
}

func TestValidateFailClosed(t *testing.T) {
	bad := BuilderProfile{SchemaVersion: SchemaVersion, Identity: "x", HostControlClass: "nonsense",
		Fields: []Field{{Name: "a", Value: "b"}}}
	if code(bad.Validate()) != nkderr.CodeInvalidCatalog {
		t.Fatal("invalid hostControlClass must fail closed")
	}
	noFields := BuilderProfile{SchemaVersion: SchemaVersion, Identity: "x", HostControlClass: OperatorControlled}
	if code(noFields.Validate()) != nkderr.CodeInvalidCatalog {
		t.Fatal("a profile with no fields must fail closed")
	}
	if err := Embedded("amd64").Validate(); err != nil {
		t.Fatalf("the embedded default profile must validate: %v", err)
	}
}

// ADR-18 §4 / ADR-21 §5: repinning the runner digest is a builder-identity change
// (fail-closed field) and MUST change the profile digest.
func TestRepinRunnerChangesDigest(t *testing.T) {
	p := Embedded("amd64")
	before, err := p.Digest()
	if err != nil {
		t.Fatal(err)
	}
	p.Repin(FieldRunnerDigest, "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false)
	after, err := p.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("repinning the runner digest must change the profile digest")
	}
	// the field is hard-fail (definitionally output-affecting)
	for _, f := range p.Fields {
		if f.Name == FieldRunnerDigest && (!f.HardFail || f.MeasurementStage != MeasurementExecution) {
			t.Fatal("runner digest field must be hard-fail and execution-measured")
		}
	}
}
