// Package discoverytags owns the pure syntax shared by authored image discovery
// selections and registry publication admission. It does not admit membership,
// perform I/O, or establish authority for a mutable tag.
package discoverytags

import (
	"regexp"
	"sort"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

const maxTags = 4096

var (
	outputPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)
	tagPattern    = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$`)
)

// Validate rejects noncanonical output names, nonliteral OCI tags, and the
// reserved signature/evidence tag namespace before filesystem or registry work.
func Validate(tags map[string]string) error {
	if len(tags) > maxTags {
		return e.New(e.CodeInvalidCatalog, "image discovery tags exceed the publication object bound")
	}
	outputs := make([]string, 0, len(tags))
	for output := range tags {
		outputs = append(outputs, output)
	}
	sort.Strings(outputs)
	for _, output := range outputs {
		tag := tags[output]
		if !outputPattern.MatchString(output) || !tagPattern.MatchString(tag) || strings.HasPrefix(tag, "sha256-") {
			return e.New(e.CodeInvalidCatalog, "image discovery tag for %q requires a canonical output name and literal OCI tag outside the reserved sha256-* namespace", output)
		}
	}
	return nil
}
