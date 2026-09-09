package pipeline

import (
	"context"
	"sort"
	"sync/atomic"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/ir"
	"github.com/nhensley/nkdctl/pkg/nkd/profile"
)

const reportQualificationDomain = canonical.KindQualifiedReport

type reportQualification struct {
	profile     profile.BuilderProfile
	receipt     profile.ExecutionReceipt
	buildNodes  []ir.Node
	terminal    terminalVerificationReceipt
	binding     string
	profileFree bool
	valid       bool
	closed      *atomic.Bool
}

type qualifiedReportView struct {
	RecipeID         string           `json:"recipeId"`
	RecipeDigest     string           `json:"recipeDigest"`
	ProfileDigest    string           `json:"profileDigest,omitempty"`
	ExecutionBinding string           `json:"executionBinding,omitempty"`
	Artifacts        []ArtifactResult `json:"artifacts"`
}

// Qualified reports whether the report currently carries a valid-looking opaque
// qualification marker. Authority consumers must call RequireQualified as well: it
// rechecks the receipt and the report's exact artifact binding.
func (report *Report) Qualified() bool {
	return report != nil && report.qualification.available()
}

func (qualification reportQualification) available() bool {
	return qualification.valid && qualification.closed != nil && !qualification.closed.Load()
}

// Close revokes this report's same-process qualification capability. The shared
// atomic revocation token also invalidates shallow copies, while the receipt itself
// stays immutable for race-safe validation. Close is idempotent and owns no paths.
func (report *Report) Close() error {
	if report != nil && report.qualification.closed != nil {
		report.qualification.closed.Store(true)
	}
	return nil
}

// QualificationProfileDigest is empty for a profile-free, byte-preserving prebuilt
// acquisition. It does not itself authorize use; call RequireQualified first.
func (report *Report) QualificationProfileDigest() string {
	if report == nil || !report.qualification.available() || report.qualification.profileFree {
		return ""
	}
	return report.qualification.receipt.ProfileDigest()
}

// ExecutionBinding identifies the actual measured execution used by every
// from-source artifact in this report. It is empty for profile-free acquisition.
func (report *Report) ExecutionBinding() string {
	if report == nil || !report.qualification.available() || report.qualification.profileFree {
		return ""
	}
	return report.qualification.receipt.ExecutionBinding()
}

// RequireQualified fails closed for a dry-run, development run, forged/zero
// receipt, changed profile/node selection, or report mutation after qualification.
func (report *Report) RequireQualified(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "qualified report validation requires a non-nil context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "qualified report validation canceled")
	}
	if report == nil || !report.qualification.available() {
		return e.New(e.CodePreflightFailed,
			"report is unqualified; development or dry-run output cannot authorize a validation vector")
	}
	if err := report.qualification.terminal.require(ctx, report); err != nil {
		return err
	}
	if !report.qualification.profileFree {
		if err := report.qualification.receipt.RequireFor(ctx, report.qualification.profile, report.qualification.buildNodes); err != nil {
			return err
		}
	}
	profileDigest, executionBinding := "", ""
	if !report.qualification.profileFree {
		profileDigest = report.qualification.receipt.ProfileDigest()
		executionBinding = report.qualification.receipt.ExecutionBinding()
	}
	binding, err := reportQualificationBinding(report, profileDigest, executionBinding)
	if err != nil {
		return err
	}
	if binding != report.qualification.binding {
		return e.New(e.CodeVerifyFailed, "qualified report artifacts changed after terminal verification")
	}
	return nil
}

func bindQualifiedReport(ctx context.Context, report *Report, execution *verifiedExecution, terminal terminalVerificationReceipt) error {
	if report == nil || execution == nil || !terminal.valid {
		return e.New(e.CodePreflightFailed, "from-source report qualification requires verified execution")
	}
	if err := terminal.require(ctx, report); err != nil {
		return err
	}
	if err := execution.receipt.RequireFor(ctx, execution.profile, execution.nodes); err != nil {
		return err
	}
	byComponent := make(map[string]ArtifactResult, len(report.Artifacts))
	for _, artifact := range report.Artifacts {
		if _, duplicate := byComponent[artifact.Component]; duplicate {
			return e.New(e.CodeVerifyFailed, "report contains duplicate artifact component %q", artifact.Component)
		}
		byComponent[artifact.Component] = artifact
	}
	expectedComponents := make(map[string]bool, len(execution.receipt.NodeIdentities()))
	for _, identity := range execution.receipt.NodeIdentities() {
		expectedComponents[identity.Component] = true
		artifact, ok := byComponent[identity.Component]
		if !ok {
			return e.New(e.CodeVerifyFailed, "qualified build node %q produced no report artifact", identity.NodeID)
		}
		if artifact.Qualification != ArtifactQualifiedExecution || artifact.BuilderType == "" || artifact.EngineClass != identity.EngineClass {
			return e.New(e.CodeVerifyFailed,
				"artifact %q does not carry its verified engine identity", identity.Component)
		}
		if artifact.ProfileDigest != execution.receipt.ProfileDigest() || artifact.ExecutionBinding != execution.receipt.ExecutionBinding() {
			return e.New(e.CodeVerifyFailed,
				"artifact %q does not carry the exact execution-receipt binding", identity.Component)
		}
	}
	for _, artifact := range report.Artifacts {
		if artifact.BuilderType != "" && !expectedComponents[artifact.Component] {
			return e.New(e.CodeVerifyFailed,
				"report contains unselected from-source artifact %q", artifact.Component)
		}
		if artifact.BuilderType == "" && artifact.Qualification != ArtifactProfileFreeAcquisition {
			return e.New(e.CodeVerifyFailed,
				"mixed report artifact %q is not an explicit byte-preserving acquisition", artifact.Component)
		}
	}
	binding, err := reportQualificationBinding(report, execution.receipt.ProfileDigest(), execution.receipt.ExecutionBinding())
	if err != nil {
		return err
	}
	report.qualification = reportQualification{
		profile: cloneProfile(execution.profile), receipt: execution.receipt,
		buildNodes: cloneBuildNodes(execution.nodes), terminal: terminal,
		binding: binding, valid: true, closed: &atomic.Bool{},
	}
	return nil
}

func bindProfileFreeReport(ctx context.Context, report *Report, terminal terminalVerificationReceipt) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "profile-free report qualification requires a non-nil context")
	}
	if report == nil {
		return e.New(e.CodeVerifyFailed, "profile-free acquisition returned no report")
	}
	if err := terminal.require(ctx, report); err != nil {
		return err
	}
	for _, artifact := range report.Artifacts {
		if artifact.Qualification != ArtifactProfileFreeAcquisition || artifact.BuilderType != "" || artifact.ProfileDigest != "" || artifact.ExecutionBinding != "" {
			return e.New(e.CodeVerifyFailed,
				"artifact %q is not a byte-preserving profile-free acquisition", artifact.Component)
		}
		if artifact.EngineClass != ir.EnginePrebuiltAcquire {
			return e.New(e.CodeVerifyFailed,
				"profile-free artifact %q has engineClass %q, want %q",
				artifact.Component, artifact.EngineClass, ir.EnginePrebuiltAcquire)
		}
	}
	binding, err := reportQualificationBinding(report, "", "")
	if err != nil {
		return err
	}
	report.qualification = reportQualification{
		terminal: terminal, binding: binding, profileFree: true, valid: true, closed: &atomic.Bool{},
	}
	return nil
}

func reportQualificationBinding(report *Report, profileDigest, executionBinding string) (string, error) {
	artifacts := append([]ArtifactResult(nil), report.Artifacts...)
	for i := range artifacts {
		artifacts[i].AbsPath = ""
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Component < artifacts[j].Component })
	view := qualifiedReportView{
		RecipeID: report.RecipeID, RecipeDigest: report.RecipeDigest,
		ProfileDigest: profileDigest, ExecutionBinding: executionBinding,
		Artifacts: artifacts,
	}
	return canonical.Digest(reportQualificationDomain, view)
}
