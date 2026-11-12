// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package tektonrun connects authenticated scheduling to the shared build,
// artifact qualification and release-manifest owners. It owns no build policy.
package tektonrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releasemanifest"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// Execute admits the generated task before any build or evidence operation.
func Execute(ctx context.Context, options factory.TektonTaskOptions) error {
	return factory.ExecuteTektonTaskWithEvidence(ctx, options, lifecycle.ValidateTektonPlan, evidence)
}

func evidence(ctx context.Context, task *factory.TektonEvidenceTask) (result error) {
	plan, err := task.Plan(ctx)
	if err != nil {
		return err
	}
	binary, pin, err := task.Cosign(ctx)
	if err != nil {
		return err
	}
	policy, err := lifecycle.TektonPolicyForPlan(plan, pin)
	if err != nil {
		return err
	}
	session, err := lifecycle.OpenTektonQualification(ctx, task, policy)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, session.Close()) }()
	operation, err := task.Operation(ctx)
	if err != nil {
		return err
	}
	if operation == "smoke" {
		receipt, _, err := session.RunNativeSmoke(ctx)
		if err != nil {
			return err
		}
		return session.WriteNativeSmoke(ctx, receipt)
	}
	credential := sign.Credential{}
	if !plan.Environment.Publication.Anonymous {
		credential = sign.Credential{Registry: strings.Split(plan.Environment.Publication.RepositoryPrefix, "/")[0], Username: os.Getenv("NVKR_REGISTRY_USER"), Password: os.Getenv("NVKR_CREDENTIAL")}
		if credential.Username == "" || credential.Password == "" {
			return e.New(e.CodeTrustPolicyMissing, "Tekton publication requires an explicit registry credential")
		}
	}
	digest, err := policy.Digest()
	if err != nil {
		return err
	}
	identity, err := policy.Tekton.Identity("qualifier")
	if err != nil {
		return err
	}
	manifestPolicy := releasemanifest.Policy{QualificationIdentity: identity, QualificationPolicyDigest: digest}
	issuerOptions := releasemanifest.TektonOptions{Options: releasemanifest.Options{CosignBinary: binary, CosignDigest: pin, Credential: credential}, Trust: *policy.Tekton, PlainHTTP: plan.Environment.Publication.PlainHTTP}
	directory, err := task.EvidenceDirectory(ctx, "qualification")
	if err != nil {
		return err
	}
	if operation == "qualify" {
		smoke, err := session.AuthenticateNativeSmoke(ctx)
		if err != nil {
			return err
		}
		publisher, err := qualification.NewTektonPublisher(qualification.TektonOptions{Options: qualification.Options{CosignBinary: binary, CosignDigest: pin, Credential: credential}, Trust: *policy.Tekton, PrivateKeyPath: "/nvkr/keys/private.pem", PlainHTTP: plan.Environment.Publication.PlainHTTP})
		if err != nil {
			return err
		}
		published, err := publisher.PublishTekton(ctx, session.Checks, session.Invocation, smoke, qualification.Policy{QualificationPolicyDigest: digest})
		if err != nil {
			return err
		}
		if !published.Successful || published.Evidence == nil {
			return e.New(e.CodeReleaseIncomplete, "Tekton qualification did not publish complete evidence")
		}
		raw, err := published.Evidence.Canonical()
		if err != nil {
			return err
		}
		issuerOptions.PrivateKeyPath = "/nvkr/keys/private.pem"
		issuer, err := releasemanifest.NewTektonIssuer(issuerOptions)
		if err != nil {
			return err
		}
		receipt, err := issuer.IssueTekton(ctx, session.Checks, session.Invocation, raw, manifestPolicy)
		if err != nil {
			return err
		}
		manifest, err := receipt.Canonical()
		if err != nil {
			return err
		}
		return lifecycle.WriteTektonEvidence(ctx, directory, map[string][]byte{"evidence.json": raw, "manifest.json": manifest})
	}
	if operation != "finalize" {
		return e.New(e.CodeEngineCapability, "unknown Tekton evidence operation")
	}
	raw, err := lifecycle.ReadTektonEvidence(ctx, filepath.Join(directory, "evidence.json"))
	if err != nil {
		return err
	}
	manifest, err := lifecycle.ReadTektonEvidence(ctx, filepath.Join(directory, "manifest.json"))
	if err != nil {
		return err
	}
	claim, err := qualification.Decode(raw)
	if err != nil {
		return err
	}
	uid, err := task.CompletedTaskUID(ctx, "nvkr-qualify")
	if err != nil {
		return err
	}
	if claim.Invocation.TaskUID != uid {
		return e.New(e.CodeUntrustedSigner, "Tekton qualification names another provider task")
	}
	issuer, err := releasemanifest.NewTektonIssuer(issuerOptions)
	if err != nil {
		return err
	}
	verified, err := issuer.VerifyTekton(ctx, session.Checks, raw, manifest, manifestPolicy)
	if err != nil {
		return err
	}
	exact, err := verified.Canonical()
	if err != nil {
		return err
	}
	output, err := task.EvidenceDirectory(ctx, "result")
	if err != nil {
		return err
	}
	return lifecycle.WriteTektonEvidence(ctx, output, map[string][]byte{"manifest.json": exact})
}
