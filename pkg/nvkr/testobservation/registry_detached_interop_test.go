// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package testobservation

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
	containerregistry "github.com/google/go-containerregistry/pkg/registry"
)

// This private stock local-key fixture proves standard carrier interoperability.
// It does not issue production keyless identities or prove hosted OIDC acceptance.
type stockLocalBundleFixture struct{ binary, workspace string }

func (f *stockLocalBundleFixture) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, f.binary, args...)
	cmd.Dir = f.workspace
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + f.workspace, "DOCKER_CONFIG=" + f.workspace, "COSIGN_PASSWORD="}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, e.Wrap(e.CodeSignatureInvalid, err, "stock fixture cosign %s: %s", args[0], stderr.String())
	}
	return out, nil
}
func (f *stockLocalBundleFixture) CreateTestObservationBundle(ctx context.Context, s sign.Subject, p []byte) ([]byte, error) {
	statement, err := canonical.Marshal(struct {
		Type          string          `json:"_type"`
		PredicateType string          `json:"predicateType"`
		Predicate     json.RawMessage `json:"predicate"`
		Subjects      []struct {
			Name   string            `json:"name"`
			Digest map[string]string `json:"digest"`
		} `json:"subject"`
	}{"https://in-toto.io/Statement/v1", PredicateType, json.RawMessage(p), []struct {
		Name   string            `json:"name"`
		Digest map[string]string `json:"digest"`
	}{{s.Ref, map[string]string{"sha256": strings.TrimPrefix(s.Digest, "sha256:")}}}})
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(f.workspace, "statement.json"), statement, 0600); err != nil {
		return nil, err
	}
	if _, err := f.run(ctx, "attest-blob", "--yes", "--statement", "statement.json", "--bundle", "bundle.json", "--new-bundle-format", "--key", "cosign.key", "--use-signing-config=false", "--tlog-upload=false"); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(f.workspace, "bundle.json"))
}
func (f *stockLocalBundleFixture) VerifyTestObservationBundle(ctx context.Context, s sign.Subject, p, b []byte) error {
	if err := os.WriteFile(filepath.Join(f.workspace, "verify.json"), b, 0600); err != nil {
		return err
	}
	if _, err := f.run(ctx, "verify-blob-attestation", "--bundle", "verify.json", "--type", PredicateType, "--digest", strings.TrimPrefix(s.Digest, "sha256:"), "--digestAlg", "sha256", "--key", "cosign.pub", "--insecure-ignore-tlog", "--offline", "--trusted-root", "trusted-root.json"); err != nil {
		return err
	}
	// Stock crypto authenticates the complete statement. The fixture also compares
	// its predicate/subject with independently supplied exact bytes, as production does.
	var bundle struct {
		Envelope struct {
			Payload []byte `json:"payload"`
		} `json:"dsseEnvelope"`
	}
	if err := json.Unmarshal(b, &bundle); err != nil {
		return err
	}
	var statement struct {
		Predicate json.RawMessage `json:"predicate"`
		Subjects  []struct {
			Name string `json:"name"`
		} `json:"subject"`
	}
	if err := json.Unmarshal(bundle.Envelope.Payload, &statement); err != nil {
		return err
	}
	if !bytes.Equal(statement.Predicate, p) || len(statement.Subjects) != 1 || statement.Subjects[0].Name != s.Ref {
		return e.New(e.CodeSignatureInvalid, "stock fixture exact predicate/subject differs")
	}
	return nil
}
func TestStockCosignDetachedObservationReferrerInterop(t *testing.T) {
	binary := os.Getenv("NVKR_TEST_COSIGN_BINARY")
	if binary == "" {
		t.Skip("set NVKR_TEST_COSIGN_BINARY to the pinned stock cosign executable")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	fixture := &stockLocalBundleFixture{binary: binary, workspace: t.TempDir()}
	if err := os.WriteFile(filepath.Join(fixture.workspace, "config.json"), []byte(`{"auths":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.workspace, "trusted-root.json"), []byte(`{"mediaType":"application/vnd.dev.sigstore.trustedroot+json;version=0.1","tlogs":[],"certificateAuthorities":[],"ctlogs":[],"timestampAuthorities":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.run(ctx, "generate-key-pair"); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(containerregistry.New(containerregistry.Logger(log.New(io.Discard, "", 0))))
	defer server.Close()
	repository := strings.TrimPrefix(server.URL, "http://") + "/observations"
	transport := delivery.RegistryOptions{PlainHTTP: true, Anonymous: true}
	authenticator := &stockObservationAuthenticator{signer: fixture, options: RegistryOptions{Transport: transport}}
	root := emitFixture(t, observationFixture(), passingJUnit)
	result, err := publishWith(ctx, root, repository, transport, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer bundle.Close()
	expectedObservation := bundle.Observation()
	expected := Expected{ObservationDigest: bundle.Digest(), Inputs: expectedObservation.Inputs, Subjects: expectedObservation.Subjects, Suite: expectedObservation.Suite}
	verified, err := verifyOCIWith(ctx, result.Locator.Reference, expected, transport, authenticator)
	if err != nil {
		t.Fatal(err)
	}
	defer verified.Close()
	download, err := fixture.run(ctx, "download", "attestation", "--predicate-type", PredicateType, result.Locator.Reference)
	if err != nil {
		t.Fatal(err)
	}
	if len(download) == 0 {
		t.Fatal("stock cosign did not discover standard ORAS referrer")
	}
	predicate, err := bundle.Canonical(ctx)
	if err != nil {
		t.Fatal(err)
	}
	subject := sign.Subject{Ref: result.Locator.Reference, Digest: strings.SplitN(result.Locator.Reference, "@", 2)[1]}
	if err := fixture.VerifyTestObservationBundle(ctx, subject, predicate, download); err != nil {
		t.Fatal(err)
	}
	requireCode(t, fixture.VerifyTestObservationBundle(ctx, subject, []byte(`{}`), download), e.CodeSignatureInvalid)
	wrong := subject
	wrong.Digest = rawPin("wrong")
	requireCode(t, fixture.VerifyTestObservationBundle(ctx, wrong, predicate, download), e.CodeSignatureInvalid)
	t.Logf("stock offline local-key + guarded ORAS carrier + stock discovery passed: %d bundle bytes", len(download))
}
