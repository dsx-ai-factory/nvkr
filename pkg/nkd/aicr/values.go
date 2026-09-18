package aicr

import e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"

var (
	serviceDomain = map[string]bool{
		"eks": true, "gke": true, "aks": true, "oke": true,
		"kind": true, "lke": true, "bcm": true,
	}
	acceleratorDomain = map[string]bool{
		"h100": true, "h200": true, "gb200": true, "b200": true,
		"a100": true, "l40": true, "l40s": true, "rtx-pro-6000": true,
	}
	osDomain       = map[string]bool{"ubuntu": true, "rhel": true, "cos": true, "amazonlinux": true, "ol": true, "talos": true}
	intentDomain   = map[string]bool{"training": true, "inference": true}
	platformDomain = map[string]bool{"kubeflow": true, "dynamo": true, "slurm": true}
)

// ValidateValues admits the concrete inputs to the pinned upstream AICR generator.
// Platform is optional. All other values must be explicit supported members.
func ValidateValues(service, accelerator, osName, intent, platform string) error {
	for _, field := range []struct {
		name   string
		value  string
		domain map[string]bool
	}{
		{"service", service, serviceDomain},
		{"accelerator", accelerator, acceleratorDomain},
		{"os", osName, osDomain},
		{"intent", intent, intentDomain},
	} {
		if field.value == "" {
			return e.New(e.CodeInvalidCatalog, "AICR profile %s is required", field.name)
		}
		if !field.domain[field.value] {
			return e.New(e.CodeUnsupported, "AICR profile %s %q is outside the supported v1 domain", field.name, field.value)
		}
	}
	if platform != "" && !platformDomain[platform] {
		return e.New(e.CodeUnsupported, "AICR profile platform %q is outside the supported v1 domain", platform)
	}
	return nil
}
