package lifecycle

import (
	"errors"
	"sort"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
)

const (
	RunPending  = "pending"
	RunFailed   = "failed"
	RunComplete = "complete"
)

// PrepareRun freezes required output slots before execution. The returned Run
// is descriptive state; it is not a resumable execution or release capability.
func PrepareRun(definition Definition, environment Environment, plan *factory.Plan, attempt string) (*Run, error) {
	if plan == nil {
		return nil, e.New(e.CodeUsage, "run preparation requires a compiled plan")
	}
	if !namePattern.MatchString(attempt) {
		return nil, e.New(e.CodeUsage, "run preparation requires a canonical attempt name")
	}
	definitionDigest, err := definition.Digest()
	if err != nil {
		return nil, err
	}
	environmentDigest, err := environment.Digest()
	if err != nil {
		return nil, err
	}
	if plan.DefinitionDigest != definitionDigest || plan.EnvironmentDigest != environmentDigest {
		return nil, e.New(e.CodeUnverifiedRef, "compiled plan does not bind the supplied Definition and Environment")
	}
	planDigest, err := plan.Digest()
	if err != nil {
		return nil, err
	}
	run := &Run{SchemaVersion: RunSchemaVersion, Attempt: attempt, DefinitionDigest: definitionDigest,
		EnvironmentDigest: environmentDigest, PlanDigest: planDigest,
		Lane: LaneUnqualified, Status: RunPending}
	recipes := make(map[string]factory.RecipePlan, len(plan.Recipes))
	for _, recipePlan := range plan.Recipes {
		recipes[recipePlan.Name] = recipePlan
	}
	artifacts := make(map[string]factory.Artifact, len(plan.Graph.Artifacts))
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	for _, output := range definition.Outputs {
		recipePlan, ok := recipes[output.Recipe]
		if !ok {
			return nil, e.New(e.CodeUnverifiedRef, "run plan omits output %q recipe %q", output.Name, output.Recipe)
		}
		artifactID := "package/" + output.Name
		memberArtifactID := artifactID
		if output.Format == OutputExecutable || output.Format == OutputOCI {
			if destination, published := environment.Publish[output.Name]; published && environment.Destinations[destination].Kind == factory.TargetOCI {
				artifactID = "package/" + releasePackageName(output.Name)
			}
		}
		componentResults := make([]string, 0, len(output.Components))
		for _, component := range output.Components {
			resultID := "result/" + output.Recipe + "/" + component
			result, exists := artifacts[resultID]
			if !exists || result.Component != component || result.RecipeTarget != output.Recipe {
				return nil, e.New(e.CodeUnverifiedRef, "run plan omits exact producer result %q for output %q", resultID, output.Name)
			}
			componentResults = append(componentResults, resultID)
		}
		run.Expected = append(run.Expected, requiredOutput{Name: output.Name, Kind: output.Format, ArtifactID: artifactID, MemberArtifactID: memberArtifactID,
			RecipeDigest: recipePlan.RecipeDigest, Components: append([]string(nil), output.Components...), ComponentResults: componentResults})
	}
	sort.Slice(run.Expected, func(i, j int) bool { return run.Expected[i].Name < run.Expected[j].Name })
	return run, nil
}

// bindObserved returns a new descriptive Run after checking exact observations
// against the frozen slots. Observations never mutate or shrink requirements.
func bindObserved(run *Run, artifacts []Artifact) (*Run, error) {
	if err := validateRun(run); err != nil {
		return nil, err
	}
	if run.Status != RunPending || len(run.Observed) != 0 {
		return nil, e.New(e.CodeImmutableViolation, "run attempt has already recorded observations; create an explicit new attempt")
	}
	copyRun := *run
	copyRun.Expected = cloneRequired(run.Expected)
	copyRun.Observed = nil
	required := make(map[string]requiredOutput, len(run.Expected))
	for _, slot := range run.Expected {
		required[slot.Name] = slot
	}
	seen := make(map[string]bool, len(artifacts))
	for _, artifact := range artifacts {
		if err := artifact.validate(); err != nil {
			return nil, err
		}
		slot, ok := required[artifact.Name]
		if !ok {
			return nil, e.New(e.CodeReleaseIncomplete, "run observed unexpected artifact %q", artifact.Name)
		}
		if seen[artifact.Name] {
			return nil, e.New(e.CodeReleaseIncomplete, "run observed artifact %q more than once", artifact.Name)
		}
		if artifact.Kind != slot.Kind || artifact.RecipeDigest != slot.RecipeDigest || artifact.Role != slot.Role || !equalStrings(artifact.Components, slot.Components) {
			return nil, e.New(e.CodeVerifyFailed, "run artifact %q does not satisfy its frozen kind/recipe/component slot", artifact.Name)
		}
		seen[artifact.Name] = true
		copyRun.Observed = append(copyRun.Observed, artifact)
	}
	sort.Slice(copyRun.Observed, func(i, j int) bool { return copyRun.Observed[i].Name < copyRun.Observed[j].Name })
	if len(seen) == len(required) {
		copyRun.Status = RunComplete
	} else {
		copyRun.Status = RunFailed
		missing := e.New(e.CodeReleaseIncomplete, "run attempt did not observe every frozen output")
		copyRun.Failure = &runFailure{Code: string(e.CodeReleaseIncomplete), Message: missing.Error()}
	}
	return &copyRun, nil
}

func validateRun(run *Run) error {
	if run == nil || run.SchemaVersion != RunSchemaVersion || !namePattern.MatchString(run.Attempt) || !canonical.IsDigest(run.DefinitionDigest) ||
		!canonical.IsDigest(run.EnvironmentDigest) || !canonical.IsDigest(run.PlanDigest) || len(run.Expected) == 0 {
		return e.New(e.CodeVerifyFailed, "run does not contain a complete frozen input and output description")
	}
	switch run.Status {
	case RunPending:
		if run.Failure != nil || len(run.Observed) != 0 {
			return e.New(e.CodeVerifyFailed, "pending run contains terminal state")
		}
	case RunFailed:
		if run.Failure == nil || run.Failure.Code == "" || run.Failure.Message == "" {
			return e.New(e.CodeVerifyFailed, "failed run omits its coded failure")
		}
	case RunComplete:
		if run.Failure != nil || len(run.Observed) != len(run.Expected) {
			return e.New(e.CodeVerifyFailed, "complete run does not contain exact observations")
		}
	default:
		return e.New(e.CodeVerifyFailed, "run status %q is unsupported", run.Status)
	}
	previous := ""
	for _, slot := range run.Expected {
		if !namePattern.MatchString(slot.Name) || slot.Name <= previous || slot.ArtifactID == "" || slot.MemberArtifactID == "" || !canonical.IsDigest(slot.RecipeDigest) ||
			(slot.Kind != OutputHelm && len(slot.Components) == 0) || len(slot.ComponentResults) != len(slot.Components) {
			return e.New(e.CodeVerifyFailed, "run required outputs must be canonical, sorted, unique, and complete")
		}
		previous = slot.Name
	}
	return nil
}

func failedRun(run *Run, cause error) *Run {
	copyRun := *run
	copyRun.Expected = cloneRequired(run.Expected)
	copyRun.Observed = nil
	copyRun.Status = RunFailed
	code := e.CodeInternal
	var coded *e.Error
	if errors.As(cause, &coded) {
		code = coded.Code
	}
	copyRun.Failure = &runFailure{Code: string(code), Message: cause.Error()}
	return &copyRun
}

func (artifact Artifact) validate() error {
	if !namePattern.MatchString(artifact.Name) || artifact.Kind == "" || !canonical.IsDigest(artifact.Digest) ||
		(artifact.RecipeDigest != "" && !canonical.IsDigest(artifact.RecipeDigest)) || sortedUnique("artifact components", artifact.Components) != nil {
		return e.New(e.CodeVerifyFailed, "artifact %q must identify exact immutable bytes", artifact.Name)
	}
	return nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func cloneRequired(source []requiredOutput) []requiredOutput {
	clone := make([]requiredOutput, len(source))
	for index, slot := range source {
		clone[index] = slot
		clone[index].Components = append([]string(nil), slot.Components...)
		clone[index].ComponentResults = append([]string(nil), slot.ComponentResults...)
	}
	return clone
}
