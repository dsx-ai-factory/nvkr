// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testobservation

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	"github.com/google/go-containerregistry/pkg/registry"
)

// Local-key interoperability is a protocol fixture. Production does not expose
// this adapter and requires Linux pinned keyless certificate/issuer verification.
type cosignKeyFixture struct{ binary, workspace string }

func (f cosignKeyFixture) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, f.binary, args...)
	cmd.Dir = f.workspace
	if err := ownedcmd.ConfigureCancellation(cmd, time.Second); err != nil {
		return nil, err
	}
	cmd.Env = append(cmd.Environ(), "COSIGN_PASSWORD=")
	var diagnostic bytes.Buffer
	cmd.Stderr = &diagnostic
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("local cosign fixture: %w: %s", err, diagnostic.Bytes())
	}
	return raw, nil
}
func (f cosignKeyFixture) AttestTestObservation(ctx context.Context, subject sign.Subject, predicate []byte) error {
	path := filepath.Join(f.workspace, "observation.json")
	if err := os.WriteFile(path, predicate, 0o600); err != nil {
		return err
	}
	_, err := f.run(ctx, "attest", "--yes", "--new-bundle-format", "--type", sign.TestObservationPredicateType, "--predicate", path, "--key", "cosign.key", "--use-signing-config=false", "--tlog-upload=false", subject.Ref)
	return err
}
func (f cosignKeyFixture) VerifyTestObservation(ctx context.Context, subject sign.Subject, predicate []byte) error {
	raw, err := f.run(ctx, "download", "attestation", "--predicate-type", sign.TestObservationPredicateType, subject.Ref)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var bundle json.RawMessage
	if err := decoder.Decode(&bundle); err != nil {
		return err
	}
	path := filepath.Join(f.workspace, "bundle.json")
	if err := os.WriteFile(path, bundle, 0o600); err != nil {
		return err
	}
	// Cosign owns signature and subject/type verification. Only after its success
	// does this fixture compare the authenticated predicate to the recorded bytes.
	if _, err := f.run(ctx, "verify-blob-attestation", "--bundle", path, "--type", sign.TestObservationPredicateType, "--digest", strings.TrimPrefix(subject.Digest, "sha256:"), "--digestAlg", "sha256", "--key", "cosign.pub", "--insecure-ignore-tlog", "--offline", "--trusted-root", "trusted-root.json"); err != nil {
		return err
	}
	var decoded struct {
		Envelope struct {
			Payload string `json:"payload"`
		} `json:"dsseEnvelope"`
	}
	if err := json.Unmarshal(bundle, &decoded); err != nil {
		return err
	}
	payload, err := base64.StdEncoding.DecodeString(decoded.Envelope.Payload)
	if err != nil {
		return err
	}
	var statement struct {
		Predicate json.RawMessage `json:"predicate"`
	}
	if err := canonical.ValidateJSONDocument(payload); err != nil {
		return err
	}
	if err := json.Unmarshal(payload, &statement); err != nil {
		return err
	}
	var values any
	jsonDecoder := json.NewDecoder(bytes.NewReader(statement.Predicate))
	jsonDecoder.UseNumber()
	if err := jsonDecoder.Decode(&values); err != nil {
		return err
	}
	exact, err := canonical.Marshal(values)
	if err != nil {
		return err
	}
	if !bytes.Equal(exact, predicate) {
		return e.New(e.CodeVerifyFailed, "authenticated local-key predicate differs")
	}
	return nil
}

func TestObservationCosignIndependentProducerConsumerLoop(t *testing.T) {
	binary := os.Getenv("NVKR_TEST_COSIGN_BINARY")
	if binary == "" {
		t.Skip("set NVKR_TEST_COSIGN_BINARY for real local-key loopback interoperability")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	// Execute a real local Go suite, then record this explicit suite invocation as
	// one JUnit case. This fixture does not claim NVKE or Kubernetes runtime coverage.
	goCommand := exec.CommandContext(ctx, "go", "test", "../testresults", "-run", "^TestParseJUnitProfiles$", "-count=1")
	if err := ownedcmd.ConfigureCancellation(goCommand, time.Second); err != nil {
		t.Fatal(err)
	}
	if raw, err := goCommand.CombinedOutput(); err != nil {
		t.Fatalf("local suite failed: %v\n%s", err, raw)
	}
	o := observationFixture()
	root := emitFixture(t, o, passingJUnit)
	fixture := cosignKeyFixture{binary: binary, workspace: t.TempDir()}
	trustedRoot := []byte(`{"mediaType":"application/vnd.dev.sigstore.trustedroot+json;version=0.1","tlogs":[],"certificateAuthorities":[],"ctlogs":[],"timestampAuthorities":[]}`)
	if err := os.WriteFile(filepath.Join(fixture.workspace, "trusted-root.json"), trustedRoot, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.run(ctx, "generate-key-pair"); err != nil {
		t.Fatal(err)
	}
	server := httptestRegistry(t)
	defer server.Close()
	transport := delivery.RegistryOptions{PlainHTTP: true, Anonymous: true}
	repository := strings.TrimPrefix(server.URL, "http://") + "/portable"
	published, err := publishWith(ctx, root, repository, transport, fixture)
	if err != nil {
		t.Fatal(err)
	}
	if !published.Successful {
		t.Fatal("signed publication did not complete readback")
	}
	bundle, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Close()
	expected := Expected{ObservationDigest: bundle.Digest(), Inputs: o.Inputs, Subjects: o.Subjects, Suite: o.Suite}
	consumer := cosignKeyFixture{binary: binary, workspace: t.TempDir()}
	for _, name := range []string{"cosign.pub", "trusted-root.json"} {
		raw, err := os.ReadFile(filepath.Join(fixture.workspace, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(consumer.workspace, name), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	verified, err := verifyOCIWith(ctx, published.Locator.Reference, expected, transport, consumer)
	if err != nil {
		t.Fatal(err)
	}
	defer verified.Close()
	raw, err := verified.Read(ctx, "results/junit.xml")
	if err != nil || string(raw) != passingJUnit {
		t.Fatalf("verified raw result differs: %q, %v", raw, err)
	}
	wrongExpected := expected
	wrongExpected.Subjects = append([]Subject(nil), expected.Subjects...)
	wrongExpected.Subjects[0].PayloadDigest = rawPin("substituted")
	if receipt, err := verifyOCIWith(ctx, published.Locator.Reference, wrongExpected, transport, consumer); receipt != nil || err == nil {
		t.Fatal("wrong independently selected payload accepted")
	}
}

func httptestRegistry(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
}
