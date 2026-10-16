// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"io"
	"runtime"
	"sync/atomic"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
)

const NativeToolSmokeSchema = "nvkr.dev/tool-smoke-worker/v1"

// NativeToolSmokeOptions selects the local runtime locator. Policy supplies the
// image and fixed profiles; the host determines the native execution platform.
type NativeToolSmokeOptions struct{ RuntimeExecutable string }

// ToolSmokeMember identifies one exact delivered member of one named output.
// These descriptions confer no execution or signing authority.
type ToolSmokeMember struct {
	Output          string `json:"output"`
	Member          string `json:"member"`
	Subject         string `json:"subject"`
	BuildDigest     string `json:"buildDigest"`
	Platform        string `json:"platform"`
	PayloadDigest   string `json:"payloadDigest"`
	Size            int64  `json:"size"`
	Profile         string `json:"profile"`
	ExpectedVersion string `json:"expectedVersion"`
	ObservedVersion string `json:"observedVersion,omitempty"`
	ExpectedCommit  string `json:"expectedCommit,omitempty"`
	ObservedCommit  string `json:"observedCommit,omitempty"`
}

// NativeToolSmokeResult is failure-only diagnostic data for a known native
// member. It distinguishes an observed pass, a failed attempt, and a member
// whose execution was not reached. It cannot authorize signing.
type NativeToolSmokeResult struct {
	Output string `json:"output"`
	Member string `json:"member"`
	Status string `json:"status"`
	Code   string `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// NativeToolSmokeReport records observations for one native platform and binds
// the complete artifact checks and policy. Image is the policy's OCI reference;
// RuntimeImage is the exact native local image observed by the execution owner.
type NativeToolSmokeReport struct {
	SchemaVersion   string                          `json:"schemaVersion"`
	ArtifactChecks  ArtifactCheckDescription        `json:"artifactChecks"`
	Invocation      qualificationorigin.Description `json:"invocation"`
	PolicyDigest    string                          `json:"policyDigest"`
	Platform        string                          `json:"platform"`
	Image           string                          `json:"image"`
	RuntimeImage    string                          `json:"runtimeImage,omitempty"`
	RuntimeIdentity string                          `json:"runtimeIdentity,omitempty"`
	Members         []ToolSmokeMember               `json:"members"`
	Status          string                          `json:"status"`
	Code            string                          `json:"code,omitempty"`
	Results         []NativeToolSmokeResult         `json:"results,omitempty"`
}

// NativeToolSmokeReceipt is issued only after native execution, exact result
// evaluation and runtime cleanup. It borrows A and the invocation; closing any
// of these owners revokes use. Serialized reports cannot restore this receipt.
type NativeToolSmokeReceipt struct {
	raw        string
	checks     *ArtifactCheckReceipt
	invocation nativeSmokeInvocation
	closed     *atomic.Bool
}

func (receipt *NativeToolSmokeReceipt) Description() (NativeToolSmokeReport, error) {
	if receipt == nil || receipt.closed == nil || receipt.closed.Load() || receipt.raw == "" {
		return NativeToolSmokeReport{}, e.New(e.CodeGateUnmet, "native tool smoke receipt is not live")
	}
	checks, err := receipt.checks.Description()
	if err != nil {
		return NativeToolSmokeReport{}, err
	}
	if err := receipt.invocation.RequireSource(checks.Origin); err != nil {
		return NativeToolSmokeReport{}, err
	}
	var report NativeToolSmokeReport
	if err := canonical.UnmarshalExact([]byte(receipt.raw), &report); err != nil {
		return NativeToolSmokeReport{}, e.Wrap(e.CodeGateUnmet, err, "reading immutable native tool smoke")
	}
	return report, nil
}

func (receipt *NativeToolSmokeReceipt) Digest() (string, error) {
	report, err := receipt.Description()
	if err != nil {
		return "", err
	}
	return report.Digest()
}

func (receipt *NativeToolSmokeReceipt) Close() error {
	if receipt != nil && receipt.closed != nil {
		receipt.closed.Store(true)
	}
	return nil
}

type nativeSmokeInvocation interface {
	Description() (qualificationorigin.Description, error)
	RequireSource(qualificationorigin.Description) error
}

type nativeSmokeObservation interface {
	Profile() pipeline.ToolSmokeProfile
	Subject() (string, int64)
	Platform() string
	Image() string
	RuntimeIdentity() string
	Stdout() []byte
	Stderr() []byte
}

type nativeSmokeRuntime interface {
	run(context.Context, *heldfile.File, pipeline.ToolSmokeProfile) (nativeSmokeObservation, error)
	Close() error
}

type ownedNativeSmokeRuntime struct{ *pipeline.ToolSmokeContainer }

func (owner ownedNativeSmokeRuntime) run(ctx context.Context, subject *heldfile.File, profile pipeline.ToolSmokeProfile) (nativeSmokeObservation, error) {
	return owner.RunHeld(ctx, subject, profile)
}
func openNativeSmokeRuntime(ctx context.Context, selection pipeline.ToolSmokeSelection) (nativeSmokeRuntime, error) {
	owner, err := pipeline.OpenToolSmokeContainer(ctx, selection)
	if err != nil {
		return nil, err
	}
	return ownedNativeSmokeRuntime{owner}, nil
}

// RunNativeToolSmoke executes only held, authenticated delivered bytes. No
// executable locator, argv, credentials, signer or result injection is accepted.
func RunNativeToolSmoke(ctx context.Context, checks *ArtifactCheckReceipt, invocation *qualificationorigin.Invocation, policy QualificationPolicy, options NativeToolSmokeOptions) (*NativeToolSmokeReceipt, *NativeToolSmokeReport, error) {
	return runNativeToolSmoke(ctx, checks, invocation, policy, options, runtime.GOOS+"/"+runtime.GOARCH, openNativeSmokeRuntime)
}

func runNativeToolSmoke(ctx context.Context, checks *ArtifactCheckReceipt, invocation nativeSmokeInvocation, policy QualificationPolicy, options NativeToolSmokeOptions, platform string, open func(context.Context, pipeline.ToolSmokeSelection) (nativeSmokeRuntime, error)) (receipt *NativeToolSmokeReceipt, report *NativeToolSmokeReport, resultErr error) {
	operation, finish, err := checks.beginOperation(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer finish()
	ctx = operation
	if !qualificationNativePlatform(platform) {
		return nil, nil, e.New(e.CodeEngineCapability, "native tool smoke requires Linux AMD64 or ARM64")
	}
	rawPolicy, err := canonical.Marshal(policy)
	if err != nil {
		return nil, nil, err
	}
	var frozen QualificationPolicy
	if err := canonical.UnmarshalExact(rawPolicy, &frozen); err != nil {
		return nil, nil, e.Wrap(e.CodeTrustPolicyMissing, err, "freezing smoke policy")
	}
	digest, err := frozen.Digest()
	if err != nil {
		return nil, nil, err
	}
	if frozen.Smoke == nil {
		return nil, nil, e.New(e.CodeTrustPolicyMissing, "native tool smoke requires independent smoke policy")
	}
	description, err := checks.Description()
	if err != nil {
		return nil, nil, err
	}
	if invocation == nil {
		return nil, nil, e.New(e.CodeProvenanceMissing, "native tool smoke requires an authenticated qualification invocation")
	}
	if err := invocation.RequireSource(description.Origin); err != nil {
		return nil, nil, err
	}
	current, err := invocation.Description()
	if err != nil {
		return nil, nil, err
	}
	if err := matchNativeSmokeOrigins(description.Origin, current, frozen); err != nil {
		return nil, nil, err
	}
	members, err := qualificationSmokeMembers(checks, frozen)
	if err != nil {
		return nil, nil, err
	}
	native := make([]ToolSmokeMember, 0, len(members))
	for _, member := range members {
		if member.Platform == platform {
			native = append(native, member)
		}
	}
	if len(native) == 0 {
		return nil, nil, e.New(e.CodeEngineCapability, "publication has no tool members for native platform %q", platform)
	}
	report = &NativeToolSmokeReport{SchemaVersion: NativeToolSmokeSchema, ArtifactChecks: description, Invocation: current, PolicyDigest: digest, Platform: platform, Image: frozen.Smoke.Images[platform], Members: make([]ToolSmokeMember, 0, len(native)), Status: "fail"}
	completed, active := 0, -1
	defer func() {
		if resultErr != nil {
			projectNativeSmokeFailure(report, native, completed, active, resultErr)
		}
	}()
	if options.RuntimeExecutable == "" {
		options.RuntimeExecutable = "docker"
	}
	owner, err := open(ctx, pipeline.ToolSmokeSelection{RuntimeExecutable: options.RuntimeExecutable, Image: report.Image, Platform: platform})
	if err != nil {
		return nil, report, err
	}
	closed := false
	defer func() {
		if !closed {
			if err := owner.Close(); err != nil {
				resultErr = errors.Join(resultErr, e.Wrap(e.CodeEngineCapability, err, "closing native smoke runtime"))
			}
		}
	}()
	for index, member := range native {
		if err := nativeSmokeAlive(ctx, checks, invocation, description.Origin); err != nil {
			return nil, report, err
		}
		active = index
		observed, err := runNativeSmokeMember(ctx, checks, owner, member)
		if err != nil {
			return nil, report, err
		}
		if report.RuntimeImage != "" && (observed.Image() != report.RuntimeImage || observed.RuntimeIdentity() != report.RuntimeIdentity) {
			return nil, report, e.New(e.CodeVerifyFailed, "native smoke runtime changed between members")
		}
		report.RuntimeImage, report.RuntimeIdentity = observed.Image(), observed.RuntimeIdentity()
		member, err = evaluateNativeSmokeMember(member, observed)
		report.Members = append(report.Members, member)
		if err != nil {
			return nil, report, err
		}
		completed++
		active = -1
	}
	closeErr := owner.Close()
	closed = true
	if closeErr != nil {
		return nil, report, e.Wrap(e.CodeEngineCapability, closeErr, "closing native smoke runtime")
	}
	if err := nativeSmokeAlive(ctx, checks, invocation, description.Origin); err != nil {
		return nil, report, err
	}
	report.Status = "pass"
	raw, err := report.Canonical()
	if err != nil {
		report.Status = "fail"
		return nil, report, err
	}
	if err := nativeSmokeAlive(ctx, checks, invocation, description.Origin); err != nil {
		report.Status = "fail"
		return nil, report, err
	}
	return &NativeToolSmokeReceipt{raw: string(raw), checks: checks, invocation: invocation, closed: new(atomic.Bool)}, report, nil
}

// projectNativeSmokeFailure runs after runtime cleanup, so diagnostics include
// late cleanup/lifetime failure without changing already observed check results.
// The caller returns the original error; these descriptions grant no authority.
func projectNativeSmokeFailure(report *NativeToolSmokeReport, native []ToolSmokeMember, completed, active int, err error) {
	code := e.CodeInternal
	var coded *e.Error
	if errors.As(err, &coded) {
		code = coded.Code
	}
	report.Status, report.Code = "fail", string(code)
	report.Results = make([]NativeToolSmokeResult, len(native))
	for index, member := range native {
		result := NativeToolSmokeResult{Output: member.Output, Member: member.Member, Status: "not-evaluated", Code: string(code), Reason: "execution was not reached before native smoke failed"}
		switch {
		case index < completed:
			result.Status, result.Code, result.Reason = "pass", "", ""
		case index == active:
			result.Status, result.Reason = "fail", "delivered-tool execution or result validation failed"
		}
		report.Results[index] = result
	}
}

func nativeSmokeAlive(ctx context.Context, checks *ArtifactCheckReceipt, invocation nativeSmokeInvocation, source qualificationorigin.Description) error {
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "native tool smoke canceled")
	}
	if _, err := checks.Description(); err != nil {
		return err
	}
	if err := invocation.RequireSource(source); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "native tool smoke canceled")
	}
	return nil
}

func matchNativeSmokeOrigins(source, current qualificationorigin.Description, policy QualificationPolicy) error {
	if source.Repository != policy.Source.Repository || source.Workflow != policy.Source.Workflow || source.DefaultBranch != policy.Source.DefaultBranch ||
		current.Repository != source.Repository || current.DefaultBranch != source.DefaultBranch || current.Ref != "refs/heads/"+source.DefaultBranch || current.Workflow != policy.QualificationWorkflow || current.RunID == source.RunID {
		return e.New(e.CodeUntrustedSigner, "native smoke source or qualification invocation differs from independent policy")
	}
	return nil
}

func runNativeSmokeMember(ctx context.Context, checks *ArtifactCheckReceipt, owner nativeSmokeRuntime, member ToolSmokeMember) (nativeSmokeObservation, error) {
	return runHeldSmokeMember(ctx, checks.lifetime.verified.publication, owner, member)
}

type smokeMemberWriter interface {
	WriteExecutableMember(context.Context, string, string, io.Writer) (int64, error)
}

func runHeldSmokeMember(ctx context.Context, publication smokeMemberWriter, owner nativeSmokeRuntime, member ToolSmokeMember) (nativeSmokeObservation, error) {
	output, err := heldfile.NewOutput("qualification-tool", member.Size)
	if err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "creating bounded held tool")
	}
	defer func() { _ = output.Close() }()
	written, err := publication.WriteExecutableMember(ctx, member.Output, member.Member, output.OSFile())
	if err != nil {
		return nil, err
	}
	if written != member.Size {
		return nil, e.New(e.CodeVerifyFailed, "held smoke member has a different size")
	}
	subject, err := output.SealContext(ctx)
	if err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "sealing delivered smoke member")
	}
	defer func() { _ = subject.Close() }()
	if subject.Digest() != member.PayloadDigest || subject.Size() != member.Size {
		return nil, e.New(e.CodeVerifyFailed, "held smoke member differs from authenticated bundle")
	}
	profile, err := nativeSmokeProfile(member.Profile)
	if err != nil {
		return nil, err
	}
	return owner.run(ctx, subject, profile)
}
