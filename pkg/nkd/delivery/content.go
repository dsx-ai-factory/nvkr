package delivery

import (
	"regexp"
	"sort"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// Output describes one observed deliverable. Reference is a transport locator;
// ContentDigest excludes it from the immutable release-content identity.
type Output struct {
	Name        string      `json:"name"`
	Kind        string      `json:"kind"`
	Digest      string      `json:"digest"`
	Reference   string      `json:"reference,omitempty"`
	BuildDigest string      `json:"buildDigest"`
	Components  []string    `json:"components,omitempty"`
	Disk        *DiskOutput `json:"disk,omitempty"`
}

// DiskOutput is the descriptive copy of a binding established by factory's held
// Run observer between exact delivered QCOW2 bytes, their producer and the
// compiled Plan. Validate does not authenticate caller-composed values.
// Encryption is the producer's none/luks2 declaration; it does not describe QCOW
// container encryption, which observation rejects. ADR-20 deliberately omits
// these execution fields from release-content identity. No field claims a
// decoded RAW reproduction identity, compatibility, inspection, SBOM, or
// publication authority.
type DiskOutput struct {
	Producer   string `json:"producer"`
	Component  string `json:"component"`
	Format     string `json:"format"`
	Encryption string `json:"encryption"`
	Size       int64  `json:"size"`
	PlanDigest string `json:"planDigest"`
	Outcome    string `json:"outcome"`
}

var outputNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]*[a-z0-9])?$`)

func (output Output) Validate() error {
	if !outputNamePattern.MatchString(output.Name) || output.Kind == "" || !canonical.IsDigest(output.Digest) ||
		(output.BuildDigest != "" && !canonical.IsDigest(output.BuildDigest)) {
		return e.New(e.CodeVerifyFailed, "artifact %q must identify exact immutable bytes", output.Name)
	}
	previous := ""
	for _, component := range output.Components {
		if !outputNamePattern.MatchString(component) || component <= previous {
			return e.New(e.CodeVerifyFailed, "artifact %q components must be canonical, sorted and unique", output.Name)
		}
		previous = component
	}
	if output.Kind != "disk-image" {
		if output.Disk != nil {
			return e.New(e.CodeVerifyFailed, "artifact %q carries disk metadata for non-disk kind %q", output.Name, output.Kind)
		}
		return nil
	}
	if output.Disk == nil || !canonical.IsDigest(output.BuildDigest) ||
		!outputNamePattern.MatchString(output.Disk.Producer) ||
		!outputNamePattern.MatchString(output.Disk.Component) ||
		output.Disk.Format != "qcow2" ||
		(output.Disk.Encryption != "none" && output.Disk.Encryption != "luks2") ||
		output.Disk.Size <= 0 || !canonical.IsDigest(output.Disk.PlanDigest) ||
		output.Disk.Outcome != "succeeded" || !containsOutputComponent(output.Components, output.Disk.Component) {
		return e.New(e.CodeVerifyFailed, "disk artifact %q lacks an exact successful QCOW2 producer binding", output.Name)
	}
	return nil
}

func containsOutputComponent(components []string, component string) bool {
	for _, candidate := range components {
		if candidate == component {
			return true
		}
	}
	return false
}

// ContentDigest is the sole owner of release-content canonical bytes. It sorts
// a defensive copy and excludes repository references, so transport relocation
// cannot change content identity. Frozen output requirements are checked against
// independent observations by the Run owner before it calls this function.
func ContentDigest(outputs []Output) (string, error) {
	if len(outputs) == 0 {
		return "", e.New(e.CodeReleaseIncomplete, "release content contains no outputs")
	}
	sorted := cloneOutputs(outputs)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	previous := ""
	content := releaseContentProjection{Artifacts: make([]releaseContentOutput, 0, len(sorted))}
	for i := range sorted {
		output := &sorted[i]
		if err := output.Validate(); err != nil {
			return "", err
		}
		if output.Name == previous || !canonical.IsDigest(output.BuildDigest) {
			return "", e.New(e.CodeVerifyFailed, "release content requires unique named build outputs")
		}
		content.Artifacts = append(content.Artifacts, projectReleaseContentOutput(*output))
		previous = output.Name
	}
	return canonical.Digest(canonical.KindReleaseContent, content)
}

// releaseContentProjection is frozen by ADR-20. Runtime and transport fields,
// including disk observation metadata, remain validated but do not redefine
// the delivered release-content identity.
type releaseContentProjection struct {
	Artifacts []releaseContentOutput `json:"artifacts"`
}

type releaseContentOutput struct {
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Digest      string   `json:"digest"`
	BuildDigest string   `json:"buildDigest"`
	Components  []string `json:"components,omitempty"`
}

func projectReleaseContentOutput(output Output) releaseContentOutput {
	return releaseContentOutput{
		Name: output.Name, Kind: output.Kind, Digest: output.Digest,
		BuildDigest: output.BuildDigest, Components: append([]string(nil), output.Components...),
	}
}

func cloneOutputs(source []Output) []Output {
	result := append([]Output(nil), source...)
	for i := range result {
		result[i].Components = append([]string(nil), source[i].Components...)
		result[i].Disk = cloneDiskOutput(source[i].Disk)
	}
	return result
}

func cloneDiskOutput(source *DiskOutput) *DiskOutput {
	if source == nil {
		return nil
	}
	clone := *source
	return &clone
}
