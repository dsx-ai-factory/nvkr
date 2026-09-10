package release

import (
	"strconv"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// CompatibilityTarget is the concrete environment a release is being authorized
// for. It is input to policy evaluation, never an authorization assertion.
type CompatibilityTarget struct {
	Kubernetes string `json:"kubernetes,omitempty"`
	NKE        string `json:"nke,omitempty"`
	Platform   string `json:"platform,omitempty"`
}

// CheckCompatibility evaluates every inventory constraint against a concrete target.
// Supported version syntax is exact versions, x/* wildcards, and whitespace/comma
// separated <, <=, =, >=, > comparisons. Platform is an exact or |/comma-separated
// allow-list.
func CheckCompatibility(in *Inventory, target CompatibilityTarget) error {
	if err := in.Validate(); err != nil {
		return err
	}
	checks := []struct {
		name       string
		constraint string
		value      string
		platform   bool
	}{
		{name: "kubernetes", constraint: in.Compatibility.Kubernetes, value: target.Kubernetes},
		{name: "nke", constraint: in.Compatibility.NKE, value: target.NKE},
		{name: "platform", constraint: in.Compatibility.Platform, value: target.Platform, platform: true},
	}
	for _, check := range checks {
		if strings.TrimSpace(check.constraint) == "" {
			continue
		}
		if strings.TrimSpace(check.value) == "" {
			return e.New(e.CodeCompatMismatch, "%s compatibility requires %q but the target supplied no value", check.name, check.constraint)
		}
		var (
			matched bool
			err     error
		)
		if check.platform {
			matched = platformMatches(check.constraint, check.value)
		} else {
			matched, err = versionMatches(check.constraint, check.value)
		}
		if err != nil {
			return e.New(e.CodeCompatMismatch, "invalid %s compatibility constraint %q: %v", check.name, check.constraint, err)
		}
		if !matched {
			return e.New(e.CodeCompatMismatch, "%s %q is outside inventory compatibility %q", check.name, check.value, check.constraint)
		}
	}
	return nil
}

func checkMemberCompatibility(in *Inventory, set *ReleaseSet) error {
	for _, required := range in.Components {
		constraint := strings.TrimSpace(required.VersionConstraint)
		if constraint == "" {
			continue
		}
		for _, member := range set.Members {
			if member.Component != required.Name || (required.Role != "" && member.Role != required.Role) {
				continue
			}
			if strings.TrimSpace(member.Version) == "" {
				return e.New(e.CodeCompatMismatch, "member %s/%s/%s has no version for inventory constraint %q", member.Component, member.Role, member.Arch, constraint)
			}
			matched, err := versionMatches(constraint, member.Version)
			if err != nil {
				return e.New(e.CodeCompatMismatch, "invalid version constraint %q for member %s: %v", constraint, required.Name, err)
			}
			if !matched {
				return e.New(e.CodeCompatMismatch, "member %s/%s/%s version %q is outside inventory constraint %q", member.Component, member.Role, member.Arch, member.Version, constraint)
			}
		}
	}
	return nil
}

func platformMatches(constraint, value string) bool {
	for _, allowed := range strings.FieldsFunc(constraint, func(r rune) bool { return r == '|' || r == ',' }) {
		allowed = strings.TrimSpace(allowed)
		if allowed == "*" || allowed == value {
			return true
		}
	}
	return false
}

type version [3]int

func versionMatches(constraint, value string) (bool, error) {
	want, err := parseVersion(value, false)
	if err != nil {
		return false, err
	}
	constraint = strings.TrimSpace(constraint)
	if constraint == "*" || strings.EqualFold(constraint, "any") {
		return true, nil
	}
	if strings.Contains(constraint, "x") || strings.Contains(constraint, "X") || strings.Contains(constraint, "*") {
		parts := strings.Split(strings.TrimPrefix(constraint, "v"), ".")
		if len(parts) > 3 {
			return false, e.New(e.CodeCompatMismatch, "too many version fields")
		}
		for i, part := range parts {
			if part == "x" || part == "X" || part == "*" {
				continue
			}
			n, convErr := strconv.Atoi(part)
			if convErr != nil || n < 0 {
				return false, e.New(e.CodeCompatMismatch, "invalid wildcard version field %q", part)
			}
			if want[i] != n {
				return false, nil
			}
		}
		return true, nil
	}

	tokens := strings.Fields(strings.ReplaceAll(constraint, ",", " "))
	if len(tokens) == 0 {
		return false, e.New(e.CodeCompatMismatch, "empty version constraint")
	}
	for _, token := range tokens {
		op := "="
		raw := token
		for _, candidate := range []string{"<=", ">=", "<", ">", "="} {
			if strings.HasPrefix(token, candidate) {
				op, raw = candidate, strings.TrimPrefix(token, candidate)
				break
			}
		}
		bound, parseErr := parseVersion(raw, true)
		if parseErr != nil {
			return false, parseErr
		}
		cmp := compareVersion(want, bound)
		if (op == "=" && cmp != 0) || (op == "<" && cmp >= 0) || (op == "<=" && cmp > 0) ||
			(op == ">" && cmp <= 0) || (op == ">=" && cmp < 0) {
			return false, nil
		}
	}
	return true, nil
}

func parseVersion(raw string, allowShort bool) (version, error) {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "v")
	parts := strings.Split(raw, ".")
	if (!allowShort && len(parts) != 3) || len(parts) == 0 || len(parts) > 3 {
		return version{}, e.New(e.CodeCompatMismatch, "version %q must contain %s", raw, map[bool]string{true: "one to three numeric fields", false: "three numeric fields"}[allowShort])
	}
	var out version
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return version{}, e.New(e.CodeCompatMismatch, "version %q has invalid field %q", raw, part)
		}
		out[i] = n
	}
	return out, nil
}

func compareVersion(a, b version) int {
	for i := range a {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}
