// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sbom"
)

const TestObservationBundleMediaType = sbom.BundleMediaType

// CreateTestObservationBundle signs a fixed observation statement without any
// registry access. Registry credentials are excluded from the cosign process;
// its standard keyless bundle is independently verified before returning bytes.
// These bytes grant no passing-suite or release authority.
func (s *CosignOCISigner) CreateTestObservationBundle(ctx context.Context, subject Subject, predicate []byte) ([]byte, error) {
	if s == nil || s.runner == nil {
		return nil, e.New(e.CodeInternal, "test observation signer unavailable")
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if err := s.admit(); err != nil {
		return nil, err
	}
	if err := subject.Validate(); err != nil {
		return nil, err
	}
	if err := requireObservationPredicate(predicate); err != nil {
		return nil, err
	}
	if _, err := resolvePolicy(VerifyPolicy{RequireAttestation: true, ExpectedPredicate: predicate}, s.identity, s.issuer); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	statement, err := canonical.Marshal(struct {
		Type          string          `json:"_type"`
		PredicateType string          `json:"predicateType"`
		Predicate     json.RawMessage `json:"predicate"`
		Subjects      []struct {
			Name   string            `json:"name"`
			Digest map[string]string `json:"digest"`
		} `json:"subject"`
	}{Type: "https://in-toto.io/Statement/v1", PredicateType: TestObservationPredicateType, Predicate: json.RawMessage(predicate), Subjects: []struct {
		Name   string            `json:"name"`
		Digest map[string]string `json:"digest"`
	}{{Name: subject.Ref, Digest: map[string]string{"sha256": strings.TrimPrefix(subject.Digest, "sha256:")}}}})
	if err != nil {
		return nil, err
	}
	workspace, cleanup, err := newPrivateWorkspace("nvkr-observation-bundle-*")
	if err != nil {
		return nil, err
	}
	defer cleanup()
	environment, err := s.observationBundleEnvironment(ctx, workspace, true)
	if err != nil {
		return nil, err
	}
	var raw []byte
	if isExecRunner(s.runner) {
		raw, err = s.createObservationBundleHeld(ctx, statement, environment)
	} else {
		input, output := filepath.Join(workspace, "statement.json"), filepath.Join(workspace, "bundle.json")
		if err = writePrivateFile(ctx, input, statement); err != nil {
			return nil, err
		}
		_, err = s.capture(ctx, observationBundleArgs(input, output), environment)
		if err == nil {
			raw, err = readBoundedObservationBundle(ctx, output)
		}
		if err == nil {
			err = assertPrivateFileUnchanged(input, statement)
		}
	}
	if err != nil {
		return nil, err
	}
	if err = s.VerifyTestObservationBundle(ctx, subject, predicate, raw); err != nil {
		return nil, err
	}
	return raw, contextError(ctx)
}

func observationBundleArgs(statement, output string) []string {
	return []string{"attest-blob", "--yes", "--new-bundle-format", "--statement", statement, "--bundle", output}
}

func readBoundedObservationBundle(ctx context.Context, path string) ([]byte, error) {
	file, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "observation Sigstore bundle", MaxBytes: maxCosignOutputBytes})
	if err != nil {
		return nil, e.Wrap(e.CodeSignatureInvalid, err, "holding observation Sigstore bundle")
	}
	defer func() { _ = file.Close() }()
	raw, err := file.ReadAllContext(ctx, maxCosignOutputBytes)
	if err != nil {
		return nil, e.Wrap(e.CodeSignatureInvalid, err, "reading observation Sigstore bundle")
	}
	return raw, nil
}

func (s *CosignOCISigner) createObservationBundleHeld(ctx context.Context, statement []byte, environment []string) ([]byte, error) {
	executable, cleanup, err := snapshotPinnedCosignExecutableHeldContext(ctx, s.binary, s.binaryDigest)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	input, err := heldfile.FromBytesContext(ctx, statement, heldfile.Options{Label: "observation statement", MaxBytes: maxOCIPredicateBytes + 16384, ExpectedDigest: digestBytes(statement)})
	if err != nil {
		return nil, e.Wrap(e.CodeSignatureInvalid, err, "holding observation statement")
	}
	defer func() { _ = input.Close() }()
	output, err := heldfile.NewOutput("observation.bundle", maxCosignOutputBytes)
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "creating held observation bundle")
	}
	defer func() { _ = output.Close() }()
	statementPath, err := heldfile.ChildPath(1)
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "mapping observation statement")
	}
	outputPath, err := heldfile.ChildPath(2)
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "mapping observation bundle")
	}
	if err := runObservationHeld(ctx, executable, observationBundleArgs(statementPath, outputPath), environment, input, output); err != nil {
		return nil, err
	}
	if err := input.AssertUnchangedContext(ctx); err != nil {
		return nil, e.Wrap(e.CodeSignatureInvalid, err, "rechecking observation statement")
	}
	return captureHeldSigningOutput(ctx, output, "observation Sigstore bundle")
}

// VerifyTestObservationBundle authenticates one opaque standard keyless bundle
// against exact subject/predicate and independent certificate identity/issuer.
// It performs no registry request and receives no registry credential.
func (s *CosignOCISigner) VerifyTestObservationBundle(ctx context.Context, subject Subject, predicate, raw []byte) error {
	if s == nil || s.runner == nil {
		return e.New(e.CodeInternal, "test observation verifier unavailable")
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := s.admit(); err != nil {
		return err
	}
	if err := subject.Validate(); err != nil {
		return err
	}
	if err := requireObservationPredicate(predicate); err != nil {
		return err
	}
	policy, err := resolvePolicy(VerifyPolicy{RequireAttestation: true, ExpectedPredicate: predicate}, s.identity, s.issuer)
	if err != nil {
		return err
	}
	documents, err := decodeOCIBundles(raw)
	if err != nil {
		return err
	}
	if len(documents) != 1 {
		return e.New(e.CodeSignatureInvalid, "observation requires exactly one complete Sigstore bundle")
	}
	if _, _, err := ociBundleClaimsForSubject(documents[0], subject.Digest, TestObservationPredicateType, policy, subject.Ref); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	workspace, cleanup, err := newPrivateWorkspace("nvkr-observation-bundle-verify-*")
	if err != nil {
		return err
	}
	defer cleanup()
	environment, err := s.observationBundleEnvironment(ctx, workspace, false)
	if err != nil {
		return err
	}
	if isExecRunner(s.runner) {
		executable, cleanup, err := snapshotPinnedCosignExecutableHeldContext(ctx, s.binary, s.binaryDigest)
		if err != nil {
			return err
		}
		defer cleanup()
		input, err := heldfile.FromBytesContext(ctx, documents[0], heldfile.Options{Label: "observation bundle verification", MaxBytes: maxCosignOutputBytes, ExpectedDigest: digestBytes(documents[0])})
		if err != nil {
			return e.Wrap(e.CodeSignatureInvalid, err, "holding observation verification bundle")
		}
		defer func() { _ = input.Close() }()
		path, err := heldfile.ChildPath(1)
		if err != nil {
			return e.Wrap(e.CodeEngineCapability, err, "mapping observation verification bundle")
		}
		if err := runObservationHeld(ctx, executable, ociVerifyBundleArgs(path, subject.Digest, TestObservationPredicateType, policy), environment, input); err != nil {
			return err
		}
		if err := input.AssertUnchangedContext(ctx); err != nil {
			return e.Wrap(e.CodeSignatureInvalid, err, "rechecking observation verification bundle")
		}
	} else {
		path := filepath.Join(workspace, "bundle.json")
		if err := writePrivateFile(ctx, path, documents[0]); err != nil {
			return err
		}
		if _, err := s.capture(ctx, ociVerifyBundleArgs(path, subject.Digest, TestObservationPredicateType, policy), environment); err != nil {
			return err
		}
		if err := assertPrivateFileUnchanged(path, documents[0]); err != nil {
			return err
		}
	}
	return contextError(ctx)
}

func runObservationHeld(ctx context.Context, executable *heldfile.File, args, environment []string, inputs ...heldfile.Descriptor) error {
	var stdout, stderr cosignOutputBuffer
	if err := heldfile.RunContext(ctx, executable, args, heldfile.RunOptions{Stdout: &stdout, Stderr: &stderr, Env: environment}, inputs...); err != nil {
		if ctxErr := contextError(ctx); ctxErr != nil {
			return ctxErr
		}
		return e.Wrap(e.CodeSignatureInvalid, err, "cosign %s failed: %s", args[0], strings.TrimSpace(stderr.String()))
	}
	if stdout.exceeded || stderr.exceeded {
		return e.New(e.CodeSignatureInvalid, "cosign observation report exceeds byte budget")
	}
	return nil
}

func (s *CosignOCISigner) observationBundleEnvironment(ctx context.Context, workspace string, signing bool) ([]string, error) {
	// The existing owner creates a fresh empty private Docker config. Registry
	// credentials, including application environment mappings, never enter cosign.
	directory, err := writeEmptyObservationDockerConfig(ctx, workspace)
	if err != nil {
		return nil, err
	}
	base := ociCosignEnvironment(os.Environ(), directory)
	out := make([]string, 0, len(base))
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(name) {
		case "NVKR_REGISTRY", "NVKR_REGISTRY_USER", "NVKR_REGISTRY_PASSWORD":
			continue
		}
		out = append(out, entry)
	}
	if !signing {
		out = observationVerificationEnvironment(out)
	}
	return out, nil
}
