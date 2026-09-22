package lifecycle

import (
	"context"
	"path/filepath"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
)

// observeOutputs resolves ordinary payload paths once and independently observes
// each named output. Disk outputs consume the local execution's held capability.
func observeOutputs(ctx context.Context, workspace string, slots []requiredOutput, artifacts map[string]factory.Artifact,
	plan *factory.Plan, execution *factory.LocalRunResult,
) ([]Artifact, error) {
	var localized map[string]factory.Artifact
	observed := make([]Artifact, 0, len(slots))
	for _, slot := range slots {
		// Held disk observation must not depend on reopenable path descriptions.
		if slot.Kind != OutputDiskImage && localized == nil {
			if plan == nil {
				return nil, e.New(e.CodeInternal, "typed lifecycle package observation requires its owning plan")
			}
			localized = make(map[string]factory.Artifact, len(artifacts))
			for id, artifact := range artifacts {
				if artifact.Path != "" {
					resolved, err := lifecycleArtifactPath(artifact.Path, workspace)
					if err != nil {
						return nil, err
					}
					artifact.Path = resolved
				}
				localized[id] = artifact
			}
		}
		artifact, err := observeOutput(ctx, slot, localized, plan, execution)
		if err != nil {
			return nil, err
		}
		observed = append(observed, artifact)
	}
	return observed, nil
}

func observeOutput(ctx context.Context, slot requiredOutput, artifacts map[string]factory.Artifact,
	plan *factory.Plan, execution *factory.LocalRunResult,
) (Artifact, error) {
	if slot.Kind == OutputDiskImage {
		if execution == nil {
			return Artifact{}, e.New(e.CodeEngineCapability,
				"disk output %q requires an in-process held producer result", slot.Name)
		}
		return observeHeldDiskOutput(ctx, slot, plan, execution.ObserveDiskLifecyclePayload)
	}
	member, ok := artifacts[slot.MemberArtifactID]
	if !ok {
		return Artifact{}, e.New(e.CodeReleaseIncomplete, "run plan has no typed member for frozen output %q", slot.Name)
	}
	// Lifecycle owns the frozen result slots; factory owns re-observing their
	// exact instruction, record and payload bytes.
	if len(slot.ComponentResults) != len(slot.Components) {
		return Artifact{}, e.New(e.CodeReleaseIncomplete, "output %q has incomplete frozen producer mappings", slot.Name)
	}
	for index, id := range slot.ComponentResults {
		result, exists := artifacts[id]
		if !exists {
			return Artifact{}, e.New(e.CodeReleaseIncomplete, "output %q has no frozen producer result %q", slot.Name, id)
		}
		if result.Kind != "producer-result" || result.BuildTarget != slot.Name || result.Component != slot.Components[index] {
			return Artifact{}, e.New(e.CodeVerifyFailed, "output %q substitutes its frozen producer mapping %q", slot.Name, id)
		}
	}
	expected := factory.LifecycleOutputPlan{Name: slot.Name, Kind: slot.Kind, ArtifactID: slot.MemberArtifactID,
		BuildDigest: slot.BuildDigest, Components: append([]string(nil), slot.Components...)}
	observation, err := factory.ObserveLifecyclePayload(ctx, plan, expected, artifacts)
	if err != nil {
		return Artifact{}, err
	}
	components, digest, semantic := observation.Components, observation.Digest, observation.SemanticDigest
	if !equalStrings(components, slot.Components) {
		return Artifact{}, e.New(e.CodeVerifyFailed, "output %q bundle members differ from its frozen component mapping", slot.Name)
	}
	if slot.ArtifactID != slot.MemberArtifactID {
		outer, found := artifacts[slot.ArtifactID]
		if !found || outer.Kind != factory.PackageOCI {
			return Artifact{}, e.New(e.CodeReleaseIncomplete, "run plan has no publication root for frozen output %q", slot.Name)
		}
		inspection, inspectErr := delivery.Inspect(ctx, outer.Path)
		if inspectErr != nil {
			return Artifact{}, inspectErr
		}
		matches := 0
		for _, object := range inspection.Root.Objects {
			if object.Name == member.Name && object.Kind == member.Kind {
				matches++
				exact := object.DescriptorDigest == digest
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
	return Artifact{Name: slot.Name, Kind: slot.Kind, Digest: digest, BuildDigest: slot.BuildDigest,
		Components: components}, nil
}

type heldDiskObserver func(context.Context, *factory.Plan, factory.LifecycleOutputPlan) (factory.DiskPayloadObservation, error)

// observeHeldDiskOutput is reached in production only after observeOutput has
// required factory's concrete in-process LocalRunResult. Keeping the callback
// private makes the recording projection testable without exposing a
// caller-mintable lifecycle authority boundary.
func observeHeldDiskOutput(ctx context.Context, slot requiredOutput, plan *factory.Plan,
	observe heldDiskObserver,
) (Artifact, error) {
	expected := factory.LifecycleOutputPlan{Name: slot.Name, Kind: slot.Kind,
		ArtifactID: slot.MemberArtifactID, BuildDigest: slot.BuildDigest,
		Components: append([]string(nil), slot.Components...)}
	observation, err := observe(ctx, plan, expected)
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{Name: slot.Name, Kind: slot.Kind, Digest: observation.Digest,
		BuildDigest: observation.BuildDigest, Components: append([]string(nil), observation.Components...),
		Disk: &delivery.DiskOutput{Producer: observation.Producer, Component: observation.Component,
			Format: observation.Format, Encryption: observation.Encryption, Size: observation.Size,
			PlanDigest: observation.PlanDigest, Outcome: observation.Outcome}}, nil
}

func lifecycleArtifactPath(path, workspace string) (string, error) {
	prefix := filepath.ToSlash(filepath.Join(".nkd", "factory"))
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == prefix || len(clean) <= len(prefix)+1 || clean[:len(prefix)+1] != prefix+"/" {
		return "", e.New(e.CodePipelineDrift, "factory artifact path %q is outside %s", path, prefix)
	}
	return filepath.Join(workspace, filepath.FromSlash(clean[len(prefix)+1:])), nil
}
