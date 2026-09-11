package delivery

import (
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/google/go-containerregistry/pkg/name"
)

// Shared evidence has one canonical owner: the first sorted output membership.
// Other output memberships reference the same exact evidence through the release.
func evidenceOwner(objects []PublishedObject, binding *EvidenceBinding) (PublishedObject, error) {
	if binding == nil || len(binding.Outputs) == 0 {
		return PublishedObject{}, e.New(e.CodeVerifyFailed, "evidence has no output owner")
	}
	previous := ""
	for _, output := range binding.Outputs {
		if output <= previous {
			return PublishedObject{}, e.New(e.CodeVerifyFailed, "evidence memberships must be sorted and unique")
		}
		previous = output
	}
	for _, object := range objects {
		if object.Name == binding.Outputs[0] && object.Output != nil {
			return object, nil
		}
	}
	return PublishedObject{}, e.New(e.CodeVerifyFailed, "evidence owner is not a release output")
}

func publicationReferences(root name.Reference, publication PublicationRoot, insecure bool) (map[string]name.Digest, error) {
	refs, err := outputReferences(root, publication, insecure)
	if err != nil {
		return nil, err
	}
	for _, object := range publication.Objects {
		if object.Evidence == nil {
			continue
		}
		owner, err := evidenceOwner(publication.Objects, object.Evidence)
		if err != nil {
			return nil, err
		}
		refs[object.Name] = refs[owner.Name].Context().Digest(object.DescriptorDigest)
	}
	return refs, nil
}

// EvidenceReferences exposes exact evidence manifest locations, not trust.
// Release signature verification or independent evidence signatures are required.
func EvidenceReferences(reference string, inspection Inspection) (map[string]string, error) {
	ref, err := name.NewDigest(reference, name.StrictValidation)
	if err != nil || ref.DigestStr() != inspection.RootDigest {
		return nil, e.New(e.CodeUnpinnedSource, "evidence references require the exact release digest")
	}
	if err := inspection.Root.Validate(); err != nil {
		return nil, err
	}
	refs, err := publicationReferences(ref, inspection.Root, false)
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	for _, object := range inspection.Root.Objects {
		if object.Evidence != nil {
			result[object.Name] = refs[object.Name].Name()
		}
	}
	return result, nil
}
