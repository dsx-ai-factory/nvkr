package factory

import (
	"bytes"
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/provenance"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sbom"
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
			raw, err := readSmallFactoryFile(ctx, filepath.Join(path, "index.json"), "hosted SBOM index")
			if err != nil {
				return e.Wrap(e.CodeProvenanceMissing, err, "reading hosted SBOM index")
			}
			var index sbom.Index
			if err := canonical.UnmarshalExact(bytes.TrimSuffix(raw, []byte{'\n'}), &index); err != nil {
				return e.Wrap(e.CodeProvenanceMissing, err, "decoding hosted SBOM index")
			}
			if err := index.Validate(); err != nil {
				return err
			}
			node := nodes[artifact.Producer]
			results := map[string]pipeline.ArtifactResult{}
			for _, inputID := range node.Inputs {
				input := artifacts[inputID]
				if input.Kind != "producer-result" {
					continue
				}
				inputPath, err := local(input.Path)
				if err != nil {
					return err
				}
				result, err := readProducerResult(ctx, inputPath)
				if err != nil {
					return e.Wrap(e.CodeProvenanceMissing, err, "reading hosted producer result")
				}
				results[result.Component] = result
			}
			if len(results) != len(index.Entries) {
				return e.New(e.CodeProvenanceMissing, "hosted SBOM index does not cover every producer result")
			}
			for _, entry := range index.Entries {
				result, ok := results[entry.Artifact]
				if !ok || entry.ArtifactSHA != result.SHA256 {
					return e.New(e.CodeVerifyFailed, "hosted SBOM entry %q has the wrong subject", entry.Artifact)
				}
				bomRaw, err := readSmallFactoryFile(ctx, filepath.Join(path, entry.Artifact+".cdx.json"), "hosted per-artifact SBOM")
				if err != nil {
					return e.Wrap(e.CodeProvenanceMissing, err, "reading hosted per-artifact SBOM")
				}
				var bom sbom.BOM
				if err := canonical.UnmarshalExact(bytes.TrimSuffix(bomRaw, []byte{'\n'}), &bom); err != nil {
					return e.Wrap(e.CodeProvenanceMissing, err, "decoding hosted per-artifact SBOM")
				}
				digest, err := bom.Digest()
				if err != nil || digest != entry.SBOMDigest {
					return e.New(e.CodeVerifyFailed, "hosted per-artifact SBOM %q differs from its index", entry.Artifact)
				}
				if len(bom.Metadata.Component.Hashes) != 1 || "sha256:"+bom.Metadata.Component.Hashes[0].Content != entry.ArtifactSHA || bom.Metadata.Component.Name != entry.Artifact {
					return e.New(e.CodeVerifyFailed, "hosted SBOM subject differs from its producer")
				}
				if plan.ExecutionSettings.SBOMTool != "" {
					if len(bom.Components) == 0 {
						return e.New(e.CodeProvenanceMissing, "hosted SBOM omits required dependency inventory")
					}
					method, scanner := "", ""
					for _, property := range bom.Metadata.Props {
						if property.Name == "nkd:dependencyInspection" {
							method = property.Value
						}
						if property.Name == "nkd:dependencyScannerDigest" {
							scanner = property.Value
						}
					}
					kind := ""
					for _, inputID := range node.Inputs {
						input := artifacts[inputID]
						if input.Component == entry.Artifact && input.Kind != "producer-result" {
							kind = input.Kind
						}
					}
					if (kind == "tarball" && method != "helm-manifests/v1") || (kind != "tarball" && (method != "syft-cyclonedx/v1" || scanner != toolExecutableDigest(plan, plan.ExecutionSettings.SBOMTool))) {
						return e.New(e.CodeVerifyFailed, "hosted SBOM inspection does not match the planned scanner")
					}
				}
			}
		}
	}
	for _, recipe := range plan.Recipes {
		if !observedRecipes[recipe.RecipeDigest] {
			return e.New(e.CodeProvenanceMissing, "hosted Run omits terminal observation for recipe %q", recipe.Name)
		}
	}
	for _, route := range plan.ExecutionSettings.Routes {
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
