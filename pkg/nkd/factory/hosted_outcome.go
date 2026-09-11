package factory

import (
	"bytes"
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/provenance"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sign"
)

// ValidateHostedRestores requires the generated finalizer to account for every
// planned carrier. The status is descriptive; the typed validators below still
// reopen the restored bytes before a Run can complete.
func ValidateHostedRestores(ctx context.Context, workspace string, plan *Plan) error {
	if plan == nil {
		return e.New(e.CodeUsage, "hosted restore validation requires a plan")
	}
	for _, artifact := range plan.Graph.Artifacts {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "validating hosted restore outcomes")
		}
		path := HostedRestoreOutcomePath(workspace, artifact.ID)
		raw, err := readSmallFactoryFile(ctx, path, "hosted restore outcome")
		if err != nil {
			return e.Wrap(e.CodeProvenanceMissing, err, "hosted carrier %q has no restore outcome", artifact.ID)
		}
		if string(raw) != "restored:success\n" {
			return e.New(e.CodeProvenanceMissing, "hosted carrier %q was not restored from a successful provider job", artifact.ID)
		}
	}
	return nil
}

// HostedRestoreOutcomePath returns the generated finalizer's deterministic
// status path for one graph artifact.
func HostedRestoreOutcomePath(workspace, artifactID string) string {
	return filepath.Join(filepath.Dir(workspace), "restore-outcomes", shortStableName(artifactID)+".status")
}

// ValidateHostedOutcomes reopens the complete generated job output set using
// the owning factory schemas before a hosted Run may describe success.
func ValidateHostedOutcomes(ctx context.Context, workspace string, plan *Plan) error {
	if plan == nil {
		return e.New(e.CodeUsage, "hosted outcome validation requires a plan")
	}
	artifacts := make(map[string]Artifact, len(plan.Graph.Artifacts))
	for _, artifact := range plan.Graph.Artifacts {
		artifacts[artifact.ID] = artifact
	}
	nodes := make(map[string]Node, len(plan.Graph.Nodes))
	for _, node := range plan.Graph.Nodes {
		nodes[node.ID] = node
	}
	local := func(path string) (string, error) {
		prefix := filepath.ToSlash(filepath.Join(".nkd", "factory"))
		clean := filepath.ToSlash(filepath.Clean(path))
		if !strings.HasPrefix(clean, prefix+"/") {
			return "", e.New(e.CodePipelineDrift, "factory artifact path %q is outside generated root", path)
		}
		return filepath.Join(workspace, filepath.FromSlash(strings.TrimPrefix(clean, prefix+"/"))), nil
	}
	observedRecipes := map[string]bool{}
	evidenceSets := map[string][]delivery.ObjectInput{}
	for _, artifact := range artifacts {
		path, err := local(artifact.Path)
		if err != nil {
			return err
		}
		switch artifact.Kind {
		case string(TierTerminalObservation):
			raw, err := readSmallFactoryFile(ctx, path, "hosted terminal observation")
			if err != nil {
				return e.Wrap(e.CodeProvenanceMissing, err, "reading hosted terminal observation")
			}
			var observation factoryObservation
			if err := canonical.UnmarshalExact(bytes.TrimSuffix(raw, []byte{'\n'}), &observation); err != nil ||
				observation.SchemaVersion != "nkd.dev/factory-observation/v1" || !canonical.IsDigest(observation.RecipeDigest) || len(observation.Inputs) == 0 {
				return e.New(e.CodeProvenanceMissing, "hosted terminal observation is missing or malformed")
			}
			node := nodes[artifact.Producer]
			var recipeDigest string
			for _, recipe := range plan.Recipes {
				if recipe.Name == node.RecipeTarget {
					recipeDigest = recipe.RecipeDigest
					break
				}
			}
			if observation.Qualification != TierStaticUnqualified || observation.RecipeDigest != recipeDigest || len(observation.Inputs) != len(node.Inputs) {
				return e.New(e.CodeProvenanceMissing, "hosted terminal observation does not cover its exact planned inputs")
			}
			for index, input := range observation.Inputs {
				planned, ok := artifacts[input.ArtifactID]
				if index >= len(node.Inputs) || input.ArtifactID != node.Inputs[index] || !ok || planned.Kind != input.Kind || !canonical.IsDigest(input.Digest) {
					return e.New(e.CodeProvenanceMissing, "hosted terminal observation contains an unbound input")
				}
				inputPath, err := local(planned.Path)
				if err != nil {
					return err
				}
				actual, err := digestFactoryPath(ctx, inputPath)
				if err != nil || actual != input.Digest {
					return e.New(e.CodeVerifyFailed, "hosted terminal observation input %q differs from restored bytes", input.ArtifactID)
				}
			}
			observedRecipes[observation.RecipeDigest] = true
		case "sbom-set":
			evidence, err := sbomPublicationObjects(ctx, plan, artifact, artifacts, local)
			if err != nil {
				return err
			}
			evidenceSets[artifact.ID] = evidence
		}
	}
	for _, recipe := range plan.Recipes {
		if !observedRecipes[recipe.RecipeDigest] {
			return e.New(e.CodeProvenanceMissing, "hosted Run omits terminal observation for recipe %q", recipe.Name)
		}
	}
	for _, route := range plan.ExecutionSettings.Routes {
		packageArtifact := artifacts[route.Input]
		if packageArtifact.Kind == PackageOCI {
			layout, err := local(packageArtifact.Path)
			if err != nil {
				return err
			}
			if plan.ExecutionSettings.Signing != nil && plan.ExecutionSettings.Signing.Mode == SigningModeCosign && len(plan.LifecycleOutputs) > 0 {
				inspection, err := delivery.Inspect(ctx, layout)
				if err != nil {
					return err
				}
				if err := delivery.RequireAttestedEvidence(inspection); err != nil {
					return err
				}
			}
			var expected []delivery.ObjectInput
			packageNode := nodes[packageArtifact.Producer]
			if packageNode.Kind == NodeAttestEvidence {
				prepared, err := signNodeInput(packageNode, artifacts, PackageOCI)
				if err != nil {
					return err
				}
				packageNode = nodes[prepared.Producer]
			}
			if len(plan.LifecycleOutputs) > 0 {
				localized := make(map[string]Artifact, len(artifacts))
				for id, artifact := range artifacts {
					copy := artifact
					copy.Path, err = local(artifact.Path)
					if err != nil {
						return err
					}
					localized[id] = copy
				}
				expected, err = releaseEvidenceObjects(ctx, plan, packageNode, localized)
				if err != nil {
					return err
				}
			} else {
				for _, input := range packageNode.Inputs {
					expected = append(expected, evidenceSets[input]...)
				}
			}
			for _, evidence := range expected {
				object, raw, err := delivery.EvidenceDocument(ctx, layout, evidence.Name)
				if err != nil || object.Kind != evidence.Kind || object.SemanticDigest != evidence.SemanticDigest || !bytes.Equal(raw, evidence.Bytes) {
					return e.New(e.CodeVerifyFailed, "published SBOM evidence %q differs from finalized Run", evidence.Name)
				}
			}
		}
		publicationPath, err := local(artifacts["publication/"+route.Name].Path)
		if err != nil {
			return err
		}
		publication, err := readPublishedSubject(ctx, publicationPath)
		if err != nil {
			return e.Wrap(e.CodeProvenanceMissing, err, "reading hosted publication subject")
		}
		if plan.ExecutionSettings.Signing == nil {
			continue
		}
		signingPath, err := local(artifacts["signature/"+route.Name].Path)
		if err != nil {
			return err
		}
		record, err := readSigningRecord(ctx, signingPath)
		if err != nil {
			return e.Wrap(e.CodeProvenanceMissing, err, "reading hosted signing record")
		}
		predicatePath, err := local(artifacts["predicate/"+route.Name].Path)
		if err != nil {
			return err
		}
		predicate, err := readSmallFactoryFile(ctx, predicatePath, "hosted provenance predicate")
		if err != nil {
			return e.Wrap(e.CodeProvenanceMissing, err, "reading hosted provenance predicate")
		}
		if record.Ref != publication.Ref || record.Digest != publication.Digest || record.PredicateSHA256 != pipeline.FileDigest(predicate) ||
			record.Identity != plan.ExecutionSettings.Signing.Identity || record.Issuer != plan.ExecutionSettings.Signing.Issuer {
			return e.New(e.CodeVerifyFailed, "hosted signing record for %q differs from its publication, predicate, or policy", route.Name)
		}
		if err := validateHostedProductionPredicate(predicate, plan, plan.ExecutionSettings.Signing); err != nil {
			return err
		}
	}
	return nil
}

type hostedPredicateRun struct {
	builderID, invocationID string
	startedOn, finishedOn   time.Time
}

func (r hostedPredicateRun) BuilderID() string     { return r.builderID }
func (r hostedPredicateRun) InvocationID() string  { return r.invocationID }
func (r hostedPredicateRun) StartedOn() time.Time  { return r.startedOn }
func (r hostedPredicateRun) FinishedOn() time.Time { return r.finishedOn }

func validateHostedProductionPredicate(raw []byte, plan *Plan, signing *SigningProfile) error {
	var predicate provenance.Predicate
	trimmed := bytes.TrimSuffix(raw, []byte{'\n'})
	if err := canonical.UnmarshalExact(trimmed, &predicate); err != nil {
		return e.Wrap(e.CodeProvenanceMissing, err, "decoding hosted production predicate")
	}
	if err := provenance.ValidateProduction(predicate); err != nil {
		return err
	}
	identity, err := regexp.Compile(signing.Identity)
	if err != nil || !identity.MatchString(predicate.RunDetails.Builder.ID) {
		return e.New(e.CodeVerifyFailed, "hosted production predicate builder does not match the signing identity policy")
	}
	startedOn, err := time.Parse(time.RFC3339Nano, predicate.RunDetails.Metadata["startedOn"])
	if err != nil {
		return e.Wrap(e.CodeProvenanceMissing, err, "decoding hosted production predicate start time")
	}
	finishedOn, err := time.Parse(time.RFC3339Nano, predicate.RunDetails.Metadata["finishedOn"])
	if err != nil {
		return e.Wrap(e.CodeProvenanceMissing, err, "decoding hosted production predicate finish time")
	}
	expected, err := productionPredicateForRun(plan, signing, hostedPredicateRun{
		builderID: predicate.RunDetails.Builder.ID, invocationID: predicate.RunDetails.Metadata["invocationId"],
		startedOn: startedOn, finishedOn: finishedOn,
	})
	if err != nil {
		return err
	}
	if !bytes.Equal(trimmed, expected) {
		return e.New(e.CodeVerifyFailed, "hosted production predicate differs from the exact plan and resolved inputs")
	}
	return nil
}

func readPublishedSubject(ctx context.Context, path string) (sign.Subject, error) {
	raw, err := readSmallFactoryFile(ctx, path, "hosted published reference")
	if err != nil {
		return sign.Subject{}, err
	}
	ref := strings.TrimSpace(string(raw))
	if at := strings.LastIndexByte(ref, '@'); at > 0 {
		subject := sign.Subject{Ref: ref, Digest: ref[at+1:]}
		return subject, subject.Validate()
	}
	return sign.Subject{}, e.New(e.CodeUnpinnedSource, "hosted publication reference is not digest-pinned")
}
