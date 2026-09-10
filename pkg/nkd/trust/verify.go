package trust

import (
	"sort"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// MeasureVerify is the DEFAULT, isolated verify path (ADR-04 §3): it re-hashes
// independently-produced artifacts and compares them to the signed expected digests
// WITHOUT invoking the build engine. It operates on plain digest maps so this package
// imports no builder — the link-level isolation the ADR wants. The signature/trust
// gate (Authorize) must have already passed over the bytes `expected` came from
// (ADR-05 §3): only signed-carried digests are trusted.
//
// expected: component -> expected sha256 (from the signed ValidationVector).
// built:    component -> independently re-hashed sha256 (measured, not rebuilt here).
// It fails closed naming the first divergent component; a built component absent from
// the signed set fails closed (never silently accepted).
func MeasureVerify(expected, built map[string]string) error {
	if len(built) == 0 {
		return e.New(e.CodeVerifyFailed, "no measured artifacts to verify")
	}
	names := make([]string, 0, len(built))
	for n := range built {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w, ok := expected[n]
		if !ok {
			return e.New(e.CodeVerifyFailed, "measured component %q is not in the signed expected set", n)
		}
		if !strings.EqualFold(w, built[n]) {
			return e.New(e.CodeVerifyFailed, "component %q diverged: expected %s, measured %s", n, w, built[n])
		}
	}
	return nil
}
