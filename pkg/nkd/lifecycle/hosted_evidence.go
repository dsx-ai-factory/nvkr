package lifecycle

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/delivery"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/factory"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/sign"
)

func observeHostedPipelineEvidence(ctx context.Context, workspace string, plan *factory.Plan, artifacts map[string]factory.Artifact, observed []Artifact) error {
	if plan.Environment.Signing == nil || len(plan.Routes) == 0 {
		return e.New(e.CodeProvenanceMissing, "pipeline-attested Run has no planned signing and verification closure")
	}
	outputs := map[string]Artifact{}
	for _, artifact := range observed {
		outputs[artifact.Name] = artifact
	}
	for _, route := range plan.Routes {
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
		target := plan.Targets[route.Target]
		routed, routedFound := artifacts[route.Input]
		if !routedFound || routed.Kind != factory.PackageOCI {
			return e.New(e.CodeReleaseIncomplete, "pipeline route %q has no exact publication envelope", route.Name)
		}
		routedPath, pathErr := lifecycleArtifactPath(routed.Path, workspace)
		if pathErr != nil {
			return pathErr
		}
		if record.Ref != targetRepository(target.Ref)+"@"+record.Digest {
			return e.New(e.CodeVerifyFailed, "publication verification for %q does not bind the routed repository", route.Name)
		}
		publication, err := delivery.OpenPublication(ctx, routedPath, record.Digest)
		if err != nil {
			return err
		}
		bound, _, err := publication.Artifacts(record.Ref)
		closeErr = publication.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return e.Wrap(e.CodeVerifyFailed, closeErr, "closing checked publication")
		}
		if len(bound) != len(outputs) {
			return e.New(e.CodeVerifyFailed, "publication does not cover every observed output")
		}
		for _, member := range bound {
			observed, ok := outputs[member.Name]
			if !ok || observed.Kind != member.Kind || observed.Digest != member.Digest || observed.BuildDigest != member.BuildDigest || !equalStrings(observed.Components, member.Components) || !reflect.DeepEqual(observed.Disk, member.Disk) {
				return e.New(e.CodeVerifyFailed, "publication substitutes observed output %q", member.Name)
			}
		}
		identity, regexErr := regexp.Compile(plan.Environment.Signing.Identity)
		if regexErr != nil {
			return e.Wrap(e.CodeTrustPolicyMissing, regexErr, "compiling planned signing identity")
		}
		if record.DryRun || !record.SignatureVerified || !record.AttestationVerified || record.Identity != plan.Environment.Signing.Identity || record.Issuer != plan.Environment.Signing.Issuer ||
			!identity.MatchString(record.ObservedIdentity) || record.ObservedIssuer != plan.Environment.Signing.Issuer {
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
