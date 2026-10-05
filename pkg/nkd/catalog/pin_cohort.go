package catalog

import e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"

// Admission happens before upstream work. Equivalent-looking constraints are not
// guessed: operators review one exact selection policy for every role in a group.
func validateSameReleaseSelection(sources map[string]Source, selected map[string]bool, groups [][]string) error {
	seen := map[string]bool{}
	for _, group := range groups {
		if len(group) < 2 {
			return e.New(e.CodeUsage, "same-release groups require at least two independent sources")
		}
		first := sources[group[0]]
		for _, name := range group {
			source, ok := sources[name]
			if !ok || !selected[name] || seen[name] || source.PinnedBy != "" {
				return e.New(e.CodeUsage, "same-release source %q must be selected, independent and present in exactly one group", name)
			}
			seen[name] = true
			if source.Repo != first.Repo || source.TagPrefix != first.TagPrefix || source.Constraint != first.Constraint {
				return e.New(e.CodeInvalidCatalog, "same-release source %q must share the exact repository, tag prefix and constraint", name)
			}
		}
	}
	return nil
}

func validateSameReleasePins(pins map[string]Pin, groups [][]string) error {
	for _, group := range groups {
		first := pins[group[0]]
		for _, name := range group[1:] {
			pin := pins[name]
			if pin.Tag != first.Tag || pin.Commit != first.Commit || pin.TreeHash != first.TreeHash || pin.SourceDateEpoch != first.SourceDateEpoch {
				return e.New(e.CodeUnverifiedRef, "same-release sources %q and %q resolved different release bytes; no pins committed", group[0], name)
			}
		}
	}
	return nil
}
