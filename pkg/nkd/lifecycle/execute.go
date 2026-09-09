package lifecycle

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/nhensley/nkdctl/pkg/nkd/canonical"
	"github.com/nhensley/nkdctl/pkg/nkd/delivery"
	e "github.com/nhensley/nkdctl/pkg/nkd/errors"
	"github.com/nhensley/nkdctl/pkg/nkd/factory"
	"github.com/nhensley/nkdctl/pkg/nkd/internal/heldfile"
	"github.com/nhensley/nkdctl/pkg/nkd/pipeline"
	"github.com/nhensley/nkdctl/pkg/nkd/sign"
)

// PreparedRun is the opaque prepared lifecycle input. Callers can inspect Run;
// the generated compiler plan remains owned by this module.
type PreparedRun struct {
	definition  Definition
	environment Environment
	plan        *factory.Plan
	run         *Run
}

type LocalOptions struct {
	AttemptsRoot string
	RunnerSource string
	Jobs         int
	Runtime      string
	Go           string
	Publish      bool
	DryRun       bool
	Stdout       io.Writer
	Stderr       io.Writer
}

func PrepareFiles(ctx context.Context, definitionPath, environmentPath, generatorVersion, attempt string) (*PreparedRun, error) {
	definition, err := LoadDefinition(ctx, definitionPath)
	if err != nil {
		return nil, err
	}
	environment, err := LoadEnvironment(ctx, environmentPath)
	if err != nil {
		return nil, err
	}
	plan, err := Compile(ctx, definitionPath, definition, environmentPath, environment, generatorVersion)
	if err != nil {
		return nil, err
	}
	run, err := PrepareRun(definition, environment, plan, attempt)
	if err != nil {
		return nil, err
	}
	return &PreparedRun{definition: definition, environment: environment, plan: plan, run: run}, nil
}

// FinalizeGenerated re-admits the authored inputs and committed plan, then
// observes artifacts restored by a hosted pipeline into one immutable Run.
func FinalizeGenerated(ctx context.Context, definitionPath, environmentPath, planPath, generatorVersion, attempt, workspace, outputPath string) (*Run, error) {
	definition, err := LoadDefinition(ctx, definitionPath)
	if err != nil {
		return nil, err
	}
	environment, err := LoadEnvironment(ctx, environmentPath)
	if err != nil {
		return nil, err
	}
	expectedPlan, err := Compile(ctx, definitionPath, definition, environmentPath, environment, generatorVersion)
	if err != nil {
		return nil, err
	}
	plan, err := factory.LoadPlanContext(ctx, planPath)
	if err != nil {
		return nil, err
	}
	want, err := expectedPlan.Digest()
	if err != nil {
		return nil, err
	}
	got, err := plan.Digest()
	if err != nil {
		return nil, err
	}
	if want != got {
		return nil, e.New(e.CodePipelineDrift, "hosted Run plan %s differs from recompiled plan %s", got, want)
	}
	run, err := PrepareRun(definition, environment, plan, attempt)
	if err != nil {
		return nil, err
	}
	artifacts := make(map[string]factory.Artifact, len(plan.Graph.Artifacts))
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	if restoreErr := factory.ValidateHostedRestores(ctx, workspace, plan); restoreErr != nil {
		return recordHostedFailure(ctx, outputPath, run, restoreErr)
	}
	observed := make([]Artifact, 0, len(run.Expected))
	for _, slot := range run.Expected {
		artifact, observeErr := observeOutput(ctx, workspace, slot, artifacts, plan)
		if observeErr != nil {
			return recordHostedFailure(ctx, outputPath, run, observeErr)
		}
		observed = append(observed, artifact)
	}
	if observeErr := factory.ValidateHostedOutcomes(ctx, workspace, plan); observeErr != nil {
		return recordHostedFailure(ctx, outputPath, run, observeErr)
	}
	if plan.Qualification.String() == LanePipeline {
		if observeErr := observeHostedPipelineEvidence(ctx, workspace, plan, artifacts, observed); observeErr != nil {
			return recordHostedFailure(ctx, outputPath, run, observeErr)
		}
		run.Lane = LanePipeline
	}
	bound, err := bindObserved(run, observed)
	if err != nil {
		return recordHostedFailure(ctx, outputPath, run, err)
	}
	bound, err = bindClusterRealizations(definition, plan, bound)
	if err != nil {
		return recordHostedFailure(ctx, outputPath, run, err)
	}
	if err := writeRunRecord(ctx, outputPath, bound); err != nil {
		return nil, err
	}
	return bound, nil
}

func recordHostedFailure(ctx context.Context, outputPath string, run *Run, cause error) (*Run, error) {
	failed := failedRun(run, cause)
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	commitCtx, cancel := context.WithTimeout(base, 5*time.Second)
	defer cancel()
	if writeErr := writeRunRecord(commitCtx, outputPath, failed); writeErr != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, errors.Join(cause, writeErr), "recording hosted failed Run")
	}
	return failed, cause
}

func observeHostedPipelineEvidence(ctx context.Context, workspace string, plan *factory.Plan, artifacts map[string]factory.Artifact, observed []Artifact) error {
	if plan.ExecutionSettings.Signing == nil || len(plan.ExecutionSettings.Routes) == 0 {
		return e.New(e.CodeProvenanceMissing, "pipeline-attested Run has no planned signing and verification closure")
	}
	outputs := map[string]Artifact{}
	for _, artifact := range observed {
		outputs[artifact.Name] = artifact
	}
	for _, route := range plan.ExecutionSettings.Routes {
		artifact, ok := artifacts["verification/"+route.Name]
		if !ok {
			return e.New(e.CodeReleaseIncomplete, "pipeline-attested Run omits verification result for %q", route.Name)
		}
		path, err := lifecycleArtifactPath(artifact.Path, workspace)
		if err != nil {
			return err
		}
		held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "pipeline-publication-verification", MaxBytes: 1 << 20})
		if err != nil {
			return e.Wrap(e.CodeProvenanceMissing, err, "opening publication verification for %q", route.Name)
		}
		raw, readErr := held.ReadAllContext(ctx, 1<<20)
		closeErr := held.Close()
		if readErr != nil || closeErr != nil {
			return e.Wrap(e.CodeVerifyFailed, errors.Join(readErr, closeErr), "reading publication verification for %q", route.Name)
		}
		var record sign.VerificationRecord
		if err := canonical.UnmarshalExact(raw, &record); err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "decoding publication verification for %q", route.Name)
		}
		if err := record.Validate(); err != nil {
			return err
		}
		_, found := outputs[route.Name]
		target := plan.ExecutionSettings.Targets[route.Target]
		routed, routedFound := artifacts[route.Input]
		if !routedFound || routed.Kind != factory.PackageOCI {
			return e.New(e.CodeReleaseIncomplete, "pipeline route %q has no exact publication envelope", route.Name)
		}
		routedPath, pathErr := lifecycleArtifactPath(routed.Path, workspace)
		if pathErr != nil {
			return pathErr
		}
		envelope, inspectErr := delivery.Inspect(ctx, routedPath)
		if inspectErr != nil {
			return inspectErr
		}
		if !found || record.Digest != envelope.RootDigest || record.Ref != targetRepository(target.Ref)+"@"+envelope.RootDigest {
			return e.New(e.CodeVerifyFailed, "publication verification for %q does not bind the exact routed output", route.Name)
		}
		identity, regexErr := regexp.Compile(plan.ExecutionSettings.Signing.Identity)
		if regexErr != nil {
			return e.Wrap(e.CodeTrustPolicyMissing, regexErr, "compiling planned signing identity")
		}
		if record.DryRun || !record.SignatureVerified || !record.AttestationVerified || record.Identity != plan.ExecutionSettings.Signing.Identity || record.Issuer != plan.ExecutionSettings.Signing.Issuer ||
			!identity.MatchString(record.ObservedIdentity) || record.ObservedIssuer != plan.ExecutionSettings.Signing.Issuer {
			return e.New(e.CodeSignatureInvalid, "publication verification for %q does not prove the planned signer and provenance", route.Name)
		}
	}
	return nil
}

func targetRepository(reference string) string {
	if at := strings.IndexByte(reference, '@'); at > 0 {
		return reference[:at]
	}
	lastSlash, lastColon := strings.LastIndexByte(reference, '/'), strings.LastIndexByte(reference, ':')
	if lastColon > lastSlash {
		return reference[:lastColon]
	}
	return reference
}

func (prepared *PreparedRun) Description() Run {
	if prepared == nil || prepared.run == nil {
		return Run{}
	}
	copyRun := *prepared.run
	copyRun.Expected = cloneRequired(prepared.run.Expected)
	copyRun.Observed = cloneArtifacts(prepared.run.Observed)
	copyRun.Clusters = cloneClusterRealizations(prepared.run.Clusters)
	if prepared.run.Failure != nil {
		failure := *prepared.run.Failure
		copyRun.Failure = &failure
	}
	return copyRun
}

// ExecuteLocal performs one new attempt and independently reopens every output
// layout before binding observations. Existing attempt storage is immutable.
func ExecuteLocal(ctx context.Context, prepared *PreparedRun, options LocalOptions) (*Run, error) {
	if prepared == nil || prepared.plan == nil || prepared.run == nil {
		return nil, e.New(e.CodeUsage, "local execution requires a prepared run")
	}
	root := options.AttemptsRoot
	if root == "" {
		root = filepath.ToSlash(filepath.Join(".nkd", "runs"))
	}
	workspace := filepath.ToSlash(filepath.Join(root, prepared.run.Attempt))
	factoryOptions := factory.LocalRunOptions{Workspace: workspace, RunnerSource: options.RunnerSource,
		Jobs: options.Jobs, Runtime: options.Runtime, Go: options.Go, Publish: options.Publish,
		DryRun: options.DryRun, Stdout: options.Stdout, Stderr: options.Stderr}
	if err := factory.AdmitLocal(ctx, prepared.plan, factoryOptions); err != nil {
		return nil, err
	}
	if options.DryRun {
		if _, err := factory.RunLocal(ctx, prepared.plan, factoryOptions); err != nil {
			return nil, err
		}
		description := prepared.Description()
		return &description, nil
	}
	if !options.DryRun {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return nil, e.Wrap(e.CodeImmutableViolation, err, "creating run attempts root")
		}
		if err := os.Mkdir(workspace, 0o700); errors.Is(err, os.ErrExist) {
			return nil, e.New(e.CodeImmutableViolation, "run attempt %q already exists", prepared.run.Attempt)
		} else if err != nil {
			return nil, e.Wrap(e.CodeImmutableViolation, err, "reserving run attempt storage")
		}
	}
	result, err := factory.RunLocal(ctx, prepared.plan, factoryOptions)
	if err != nil {
		return recordFailedAttempt(ctx, workspace, prepared.run, err)
	}
	if result == nil {
		return recordFailedAttempt(ctx, workspace, prepared.run, e.New(e.CodeInternal, "local executor returned no run record"))
	}
	runForObservation := prepared.Description()
	runForObservation.Lane = result.Qualification.String()
	observed := make([]Artifact, 0, len(prepared.run.Expected))
	artifacts := make(map[string]factory.Artifact, len(prepared.plan.Graph.Artifacts))
	for _, artifact := range prepared.plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	for _, slot := range prepared.run.Expected {
		artifact, observeErr := observeOutput(ctx, workspace, slot, artifacts, prepared.plan)
		if observeErr != nil {
			return recordFailedAttempt(ctx, workspace, &runForObservation, observeErr)
		}
		observed = append(observed, artifact)
	}
	bound, err := bindObserved(&runForObservation, observed)
	if err != nil {
		return recordFailedAttempt(ctx, workspace, &runForObservation, err)
	}
	bound, err = bindClusterRealizations(prepared.definition, prepared.plan, bound)
	if err != nil {
		return recordFailedAttempt(ctx, workspace, &runForObservation, err)
	}
	if err := writeRunRecord(ctx, filepath.Join(workspace, "lifecycle.json"), bound); err != nil {
		return nil, err
	}
	return bound, nil
}

func observeOutput(ctx context.Context, workspace string, slot requiredOutput, artifacts map[string]factory.Artifact, plan *factory.Plan) (Artifact, error) {
	member, ok := artifacts[slot.MemberArtifactID]
	if !ok {
		return Artifact{}, e.New(e.CodeReleaseIncomplete, "run plan has no typed member for frozen output %q", slot.Name)
	}
	memberPath, err := lifecycleArtifactPath(member.Path, workspace)
	if err != nil {
		return Artifact{}, err
	}
	components := append([]string(nil), slot.Components...)
	digest, semantic := "", ""
	observeTypedPackage := func() (factory.LifecyclePayloadObservation, error) {
		if plan == nil {
			return factory.LifecyclePayloadObservation{}, e.New(e.CodeInternal, "typed lifecycle package observation requires its owning plan")
		}
		localized := make(map[string]factory.Artifact, len(artifacts))
		for id, candidate := range artifacts {
			copyArtifact := candidate
			if candidate.Path != "" {
				resolved, pathErr := lifecycleArtifactPath(candidate.Path, workspace)
				if pathErr != nil {
					return factory.LifecyclePayloadObservation{}, pathErr
				}
				copyArtifact.Path = resolved
			}
			localized[id] = copyArtifact
		}
		expected := factory.LifecycleOutputPlan{Name: slot.Name, Kind: slot.Kind, ArtifactID: slot.MemberArtifactID,
			RecipeDigest: slot.RecipeDigest, Components: append([]string(nil), slot.Components...)}
		return factory.ObserveLifecyclePayload(ctx, plan, expected, localized)
	}
	verifyProducer := func(index int, name, observedDigest string, observedSize int64) error {
		if index >= len(slot.ComponentResults) {
			return e.New(e.CodeReleaseIncomplete, "output %q has more observed members than frozen producer mappings", slot.Name)
		}
		resultArtifact, found := artifacts[slot.ComponentResults[index]]
		if !found {
			return e.New(e.CodeReleaseIncomplete, "output %q component %q has no producer result", slot.Name, name)
		}
		resultPath, pathErr := lifecycleArtifactPath(resultArtifact.Path, workspace)
		if pathErr != nil {
			return pathErr
		}
		producer, readErr := readArtifactResult(ctx, resultPath)
		if readErr != nil {
			return readErr
		}
		if producer.Component != name || (observedDigest != "" && (producer.SHA256 != observedDigest || producer.Size != observedSize)) {
			return e.New(e.CodeVerifyFailed, "output %q component %q differs from its independently observed producer result", slot.Name, name)
		}
		return nil
	}
	switch member.Kind {
	case factory.PackageBinaryBundle:
		bundle, result, err := delivery.InspectBinaryBundle(ctx, memberPath)
		if err != nil {
			return Artifact{}, err
		}
		components = components[:0]
		for index, entry := range bundle.Entries {
			components = append(components, entry.Name)
			if err := verifyProducer(index, entry.Name, entry.SHA256, entry.Size); err != nil {
				return Artifact{}, err
			}
		}
		digest, semantic = result.RootDigest, result.SemanticDigest
	case factory.PackageAPT:
		repository, err := delivery.InspectAPTRepository(ctx, memberPath)
		if err != nil {
			return Artifact{}, err
		}
		for index, component := range components {
			resultArtifact := artifacts[slot.ComponentResults[index]]
			var producerArtifact factory.Artifact
			for _, candidate := range artifacts {
				if candidate.Kind != "producer-result" && candidate.RecipeTarget == resultArtifact.RecipeTarget && candidate.Component == component {
					producerArtifact = candidate
					break
				}
			}
			producerPath, pathErr := lifecycleArtifactPath(producerArtifact.Path, workspace)
			if pathErr != nil {
				return Artifact{}, pathErr
			}
			held, openErr := heldfile.OpenContext(ctx, producerPath, heldfile.Options{Label: "APT producer payload", MaxBytes: 1 << 30})
			if openErr != nil {
				return Artifact{}, e.Wrap(e.CodeVerifyFailed, openErr, "opening APT producer payload")
			}
			digest, size := held.Digest(), held.Size()
			closeErr := held.Close()
			if closeErr != nil {
				return Artifact{}, e.Wrap(e.CodeVerifyFailed, closeErr, "closing APT producer payload")
			}
			if err := verifyProducer(index, component, digest, size); err != nil {
				return Artifact{}, err
			}
		}
		digest, semantic = repository.Digest, repository.Digest
		observation, observeErr := observeTypedPackage()
		if observeErr != nil {
			return Artifact{}, observeErr
		}
		if observation.Digest != digest || !equalStrings(observation.Components, components) {
			return Artifact{}, e.New(e.CodeVerifyFailed, "output %q differs from its reproduced APT payload", slot.Name)
		}
	case factory.PackageHelm:
		observation, observeErr := observeTypedPackage()
		if observeErr != nil {
			return Artifact{}, observeErr
		}
		digest, semantic = observation.Digest, observation.Digest
	default:
		return Artifact{}, e.New(e.CodeEngineCapability, "output %q package kind %q has no observation adapter", slot.Name, member.Kind)
	}
	if !equalStrings(components, slot.Components) {
		return Artifact{}, e.New(e.CodeVerifyFailed, "output %q bundle members differ from its frozen component mapping", slot.Name)
	}
	if slot.ArtifactID != slot.MemberArtifactID {
		outer, found := artifacts[slot.ArtifactID]
		if !found || outer.Kind != factory.PackageOCI {
			return Artifact{}, e.New(e.CodeReleaseIncomplete, "run plan has no publication root for frozen output %q", slot.Name)
		}
		outerPath, pathErr := lifecycleArtifactPath(outer.Path, workspace)
		if pathErr != nil {
			return Artifact{}, pathErr
		}
		inspection, inspectErr := delivery.Inspect(ctx, outerPath)
		if inspectErr != nil {
			return Artifact{}, inspectErr
		}
		matches := 0
		for _, object := range inspection.Root.Objects {
			if object.Name == member.Name && object.Kind == member.Kind {
				matches++
				exact := object.BlobDigest == digest
				if member.Kind == factory.PackageAPT {
					exact = object.SemanticDigest == semantic
				}
				if object.ArtifactType != member.MediaType || !exact {
					return Artifact{}, e.New(e.CodeVerifyFailed, "output %q publication root substitutes its planned typed member", slot.Name)
				}
			}
		}
		if matches != 1 {
			return Artifact{}, e.New(e.CodeReleaseIncomplete, "output %q publication root does not contain its exact planned binary member", slot.Name)
		}
	}
	return Artifact{Name: slot.Name, Kind: slot.Kind, Digest: digest, RecipeDigest: slot.RecipeDigest,
		Components: components}, nil
}

func lifecycleArtifactPath(path, workspace string) (string, error) {
	prefix := filepath.ToSlash(filepath.Join(".nkd", "factory"))
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == prefix || len(clean) <= len(prefix)+1 || clean[:len(prefix)+1] != prefix+"/" {
		return "", e.New(e.CodePipelineDrift, "factory artifact path %q is outside %s", path, prefix)
	}
	return filepath.Join(workspace, filepath.FromSlash(clean[len(prefix)+1:])), nil
}

func readArtifactResult(ctx context.Context, path string) (pipeline.ArtifactResult, error) {
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "lifecycle-producer-result", MaxBytes: 1 << 20})
	if err != nil {
		return pipeline.ArtifactResult{}, e.Wrap(e.CodeVerifyFailed, err, "opening producer result")
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, 1<<20)
	if err != nil {
		return pipeline.ArtifactResult{}, e.Wrap(e.CodeVerifyFailed, err, "reading producer result")
	}
	var result pipeline.ArtifactResult
	if err := canonical.UnmarshalExact(raw, &result); err != nil {
		return pipeline.ArtifactResult{}, e.Wrap(e.CodeVerifyFailed, err, "decoding producer result")
	}
	if !namePattern.MatchString(result.Component) || !canonical.IsDigest(result.SHA256) || result.Size < 0 {
		return pipeline.ArtifactResult{}, e.New(e.CodeVerifyFailed, "producer result does not identify exact component bytes")
	}
	return result, nil
}

func recordFailedAttempt(ctx context.Context, workspace string, run *Run, cause error) (*Run, error) {
	failed := failedRun(run, cause)
	base := context.Background()
	if ctx != nil {
		base = context.WithoutCancel(ctx)
	}
	commitCtx, cancel := context.WithTimeout(base, 5*time.Second)
	defer cancel()
	if err := writeRunRecord(commitCtx, filepath.Join(workspace, "lifecycle.json"), failed); err != nil {
		return nil, e.Wrap(e.CodeImmutableViolation, errors.Join(cause, err), "recording failed run attempt")
	}
	return failed, cause
}

func cloneArtifacts(source []Artifact) []Artifact {
	clone := make([]Artifact, len(source))
	for index, artifact := range source {
		clone[index] = artifact
		clone[index].Components = append([]string(nil), artifact.Components...)
	}
	return clone
}

func cloneClusterRealizations(source []clusterRealization) []clusterRealization {
	clone := make([]clusterRealization, len(source))
	for index, realization := range source {
		clone[index] = realization
		clone[index].Members = append([]clusterRealizedMember(nil), realization.Members...)
		for member := range clone[index].Members {
			clone[index].Members[member].Components = append([]string(nil), realization.Members[member].Components...)
		}
	}
	return clone
}

func writeRunRecord(ctx context.Context, path string, run *Run) error {
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "committing run record canceled")
	}
	raw, err := canonical.Marshal(run)
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".lifecycle-*.json")
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "staging immutable run record")
	}
	stage := file.Name()
	defer func() { _ = file.Close(); _ = os.Remove(stage) }()
	if _, err := file.Write(raw); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "writing run record")
	}
	if err := file.Sync(); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "syncing run record")
	}
	if err := file.Close(); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "closing run record")
	}
	if err := os.Chmod(stage, 0o444); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "protecting run record")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "committing run record canceled")
	}
	if err := os.Link(stage, path); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "committing immutable run record")
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "opening run directory")
	}
	defer func() { _ = directory.Close() }()
	if err := directory.Sync(); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "syncing run directory")
	}
	return nil
}
