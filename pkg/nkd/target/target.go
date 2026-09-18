// Package target owns concrete artifact target platforms. Target support does not
// imply that an execution host has the capabilities needed to produce an artifact.
package target

import e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"

// ValidatePlatform admits only the concrete platforms supported by the compiler.
// Empty values and wildcards never select a host-derived default.
func ValidatePlatform(platform string) error {
	switch platform {
	case "linux/amd64", "linux/arm64":
		return nil
	default:
		return e.New(e.CodeInvalidCatalog, "target platform %q must be linux/amd64 or linux/arm64", platform)
	}
}

// Arch returns the architecture of a validated platform. Invalid platforms return
// an empty value; callers must admit the platform before selecting pins or tools.
func Arch(platform string) string {
	switch platform {
	case "linux/amd64":
		return "amd64"
	case "linux/arm64":
		return "arm64"
	default:
		return ""
	}
}

func ValidArch(arch string) bool { return arch == "amd64" || arch == "arm64" }

// Arches returns the supported architecture domain in canonical order.
func Arches() []string { return []string{"amd64", "arm64"} }
