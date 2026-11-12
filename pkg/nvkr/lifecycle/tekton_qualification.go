// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"os"
	"strings"
	"sync/atomic"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationpolicy"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/sign"
)

// ValidateTektonPlan checks the shared static smoke scope before generation or
// task effects. It authenticates no provider fact or artifact bytes.
func ValidateTektonPlan(plan *factory.Plan) error {
	if plan == nil || plan.Environment.Tekton == nil {
		return e.New(e.CodeEngineCapability, "Tekton requires its explicit cluster execution policy")
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	_, err := toolSmokePlanScope(plan, QualificationSmokePolicy(plan.Environment.Tekton.Smoke))
	return err
}

// TektonPolicyForPlan describes the operator's selected cluster trust. It
// authenticates nothing; consumers independently select this complete policy
// and its Plan identity before accepting any downloaded evidence.
func TektonPolicyForPlan(plan *factory.Plan, cosignDigest string) (QualificationPolicy, error) {
	if plan == nil || plan.Environment.Tekton == nil {
		return QualificationPolicy{}, e.New(e.CodeTrustPolicyMissing, "Tekton qualification requires an explicit cluster Plan")
	}
	if err := plan.Validate(); err != nil {
		return QualificationPolicy{}, err
	}
	digest, err := plan.IdentityDigest()
	if err != nil {
		return QualificationPolicy{}, err
	}
	settings := plan.Environment.Tekton
	smoke := QualificationSmokePolicy(settings.Smoke.Clone())
	policy := QualificationPolicy{SchemaVersion: QualificationPolicySchema, Cosign: QualificationCosignPolicy{Digest: cosignDigest}, Smoke: &smoke,
		Tekton: &qualificationpolicy.Tekton{APIServer: settings.APIServer, Namespace: settings.Namespace, Platform: settings.Platform, PlanDigest: digest, PublisherKey: settings.PublisherPublicKey, SmokeKey: settings.SmokePublicKey, QualifierKey: settings.QualifierPublicKey}}
	return policy, policy.Validate()
}

// TektonInvocation borrows an authenticated factory evidence task. It cannot be
// constructed from an identity document; closing the task revokes every copy.
type TektonInvocation struct {
	ctx         context.Context
	task        *factory.TektonEvidenceTask
	source      qualificationorigin.Description
	description qualificationorigin.Description
	closed      *atomic.Bool
}

func (invocation *TektonInvocation) Description() (qualificationorigin.Description, error) {
	if invocation == nil || invocation.task == nil || invocation.closed == nil || invocation.closed.Load() {
		return qualificationorigin.Description{}, e.New(e.CodeGateUnmet, "Tekton invocation is not live")
	}
	if err := invocation.task.Require(invocation.ctx); err != nil {
		return qualificationorigin.Description{}, err
	}
	return invocation.description, nil
}

func (invocation *TektonInvocation) RequireSource(source qualificationorigin.Description) error {
	if _, err := invocation.Description(); err != nil {
		return err
	}
	if source != invocation.source {
		return e.New(e.CodeUntrustedSigner, "Tekton invocation does not bind this producing Run")
	}
	return nil
}

func (invocation *TektonInvocation) RequireSigning() error {
	description, err := invocation.Description()
	if err != nil {
		return err
	}
	if description.Task != "nvkr-qualify" {
		return e.New(e.CodeUntrustedSigner, "only the independent Tekton qualifier may issue qualification")
	}
	return nil
}

// TektonQualification owns a complete artifact-check receipt and its exact task
// invocation. Close releases held publication bytes and revokes the invocation.
type TektonQualification struct {
	Checks               *ArtifactCheckReceipt
	Invocation           *TektonInvocation
	policy               QualificationPolicy
	plan                 *factory.Plan
	task                 *factory.TektonEvidenceTask
	binary, binaryDigest string
}

func (session *TektonQualification) Close() error {
	if session == nil {
		return nil
	}
	if session.Invocation != nil && session.Invocation.closed != nil {
		session.Invocation.closed.Store(true)
	}
	return session.Checks.Close()
}

// OpenTektonQualification reuses the same complete artifact and signed-evidence
// checks as GitHub, with an explicit cluster source receipt and public-key policy.
func OpenTektonQualification(ctx context.Context, task *factory.TektonEvidenceTask, selected QualificationPolicy) (*TektonQualification, error) {
	if err := task.Require(ctx); err != nil {
		return nil, err
	}
	if err := selected.Validate(); err != nil {
		return nil, err
	}
	if selected.Tekton == nil {
		return nil, e.New(e.CodeTrustPolicyMissing, "Tekton requires independently selected cluster qualification trust")
	}
	raw, err := canonical.Marshal(selected)
	if err != nil {
		return nil, err
	}
	var policy QualificationPolicy
	if err := canonical.UnmarshalExact(raw, &policy); err != nil {
		return nil, e.Wrap(e.CodeTrustPolicyMissing, err, "freezing Tekton qualification policy")
	}
	plan, err := task.Plan(ctx)
	if err != nil {
		return nil, err
	}
	binary, pin, err := task.Cosign(ctx)
	if err != nil {
		return nil, err
	}
	want, err := TektonPolicyForPlan(plan, pin)
	if err != nil {
		return nil, err
	}
	actualDigest, err := policy.Digest()
	if err != nil {
		return nil, err
	}
	wantDigest, err := want.Digest()
	if err != nil {
		return nil, err
	}
	if actualDigest != wantDigest {
		return nil, e.New(e.CodeTrustPolicyMissing, "Tekton task differs from independently selected qualification trust")
	}
	if _, err := qualificationSmokePlanScope(plan, policy); err != nil {
		return nil, err
	}
	source, err := newTektonQualificationSource(ctx, task, plan)
	if err != nil {
		return nil, err
	}
	stage, uid, account, err := task.Identity(ctx)
	if err != nil {
		return nil, err
	}
	identity := policy.Tekton.APIServer + "/api/v1/namespaces/" + policy.Tekton.Namespace + "/serviceaccounts/" + account
	description := qualificationorigin.Description{Provider: "tekton", Task: stage, TaskUID: uid, InvocationID: source.description.InvocationID, BuilderID: identity}
	if err := description.Validate(); err != nil {
		return nil, err
	}
	reference, saved, err := factory.CollectQualificationEvidence(ctx, "/work/.nvkr/factory", plan)
	if err != nil {
		return nil, err
	}
	options := ConsumerOptions{CosignBinary: binary, CosignDigest: pin, WorkspaceRoot: "/work/.nvkr/checked", DisableMetadataCache: true,
		Registry: delivery.RegistryOptions{PlainHTTP: plan.Environment.Publication.PlainHTTP, Anonymous: plan.Environment.Publication.Anonymous}}
	if !options.Registry.Anonymous {
		user, password := os.Getenv("NVKR_REGISTRY_USER"), os.Getenv("NVKR_CREDENTIAL")
		if user == "" || password == "" {
			return nil, e.New(e.CodeTrustPolicyMissing, "Tekton evidence requires its explicit registry read credential")
		}
		options.Registry.Username, options.Registry.Password = user, password
		options.Credential = sign.Credential{Registry: strings.Split(reference, "/")[0], Username: user, Password: password}
	}
	consumer, err := NewConsumer(options)
	if err != nil {
		return nil, err
	}
	request := QualificationRequest{Reference: reference, Definition: plan.Definition, Environment: plan.Environment, Plan: plan}
	for _, predicate := range saved {
		request.Predicates = append(request.Predicates, QualificationPredicate{Reference: predicate.Reference, Predicate: predicate.Predicate})
	}
	checks, _, err := consumer.checkQualification(ctx, request, source)
	if err != nil {
		return nil, err
	}
	invocation := &TektonInvocation{ctx: ctx, task: task, source: source.description, description: description, closed: new(atomic.Bool)}
	return &TektonQualification{Checks: checks, Invocation: invocation, policy: policy, plan: plan, task: task, binary: binary, binaryDigest: pin}, nil
}

func (session *TektonQualification) RunNativeSmoke(ctx context.Context) (*NativeToolSmokeReceipt, *NativeToolSmokeReport, error) {
	if session == nil || session.Invocation == nil {
		return nil, nil, e.New(e.CodeGateUnmet, "Tekton artifact qualification is not live")
	}
	description, err := session.Invocation.Description()
	if err != nil {
		return nil, nil, err
	}
	if description.Task != "nvkr-smoke" {
		return nil, nil, e.New(e.CodeEngineCapability, "only the separate Tekton smoke task executes delivered tools")
	}
	return runNativeToolSmoke(ctx, session.Checks, session.Invocation, session.policy, NativeToolSmokeOptions{}, session.policy.Tekton.Platform,
		func(ctx context.Context, selection pipeline.ToolSmokeSelection) (nativeSmokeRuntime, error) {
			// Each task has a fresh daemon. Materialize only the policy's exact
			// anonymous image after smoke admission; the held execution owner
			// independently reauthenticates its bytes and never pulls implicitly.
			if _, err := pipeline.PrepareContainerImage(ctx, selection.RuntimeExecutable, selection.Image, selection.Platform, pipeline.ImageAnonymous); err != nil {
				return nil, err
			}
			return openNativeSmokeRuntime(ctx, selection)
		})
}
