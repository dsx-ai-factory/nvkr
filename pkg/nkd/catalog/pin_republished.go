package catalog

import (
	"maps"
	"reflect"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// This opt-in policy checks the complete candidate before either preview or CAS.
// An unchanged member may gain provenance from a newer umbrella without changing
// its payload. Transport URL changes likewise do not change acquired bytes.
func validateUnchangedVersionBytes(update SourcePinUpdate) error {
	for _, change := range update.Pins {
		before, after := change.Before, change.After
		if before.Tag == "" || NormalizeVersion(before.Tag) != NormalizeVersion(after.Tag) {
			continue
		}
		before.Tag, after.Tag = "", ""
		before.DerivedFrom, after.DerivedFrom = "", ""
		if !reflect.DeepEqual(before, after) {
			return e.New(e.CodeUnverifiedRef, "source %q republished version %q with different identity; no pins committed", change.Name, change.Before.Tag)
		}
	}
	for _, change := range update.Artifacts {
		before, after := change.Before, change.After
		if before.Tag == "" || NormalizeVersion(before.Tag) != NormalizeVersion(after.Tag) {
			continue
		}
		if before.ImageParentDigest != after.ImageParentDigest || !maps.Equal(before.Digests, after.Digests) || !reflect.DeepEqual(before.Chart, after.Chart) {
			return e.New(e.CodeUnverifiedRef, "acquired component %q republished version %q with different parent, bytes or chart identity; no pins committed", change.Name, before.Tag)
		}
	}
	return nil
}
