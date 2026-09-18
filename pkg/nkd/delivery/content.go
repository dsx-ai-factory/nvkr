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
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Digest      string   `json:"digest"`
	Reference   string   `json:"reference,omitempty"`
	BuildDigest string   `json:"buildDigest"`
	Components  []string `json:"components,omitempty"`
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
	return nil
}

// ContentDigest is the sole owner of release-content canonical bytes. It sorts
// a defensive copy and excludes repository references, so transport relocation
// cannot change content identity. Frozen output requirements are checked against
// independent observations by the Run owner before it calls this function.
func ContentDigest(outputs []Output) (string, error) {
	if len(outputs) == 0 {
		return "", e.New(e.CodeReleaseIncomplete, "release content contains no outputs")
	}
	content := struct {
		Artifacts []Output `json:"artifacts"`
	}{Artifacts: cloneOutputs(outputs)}
	sort.Slice(content.Artifacts, func(i, j int) bool { return content.Artifacts[i].Name < content.Artifacts[j].Name })
	previous := ""
	for i := range content.Artifacts {
		output := &content.Artifacts[i]
		if err := output.Validate(); err != nil {
			return "", err
		}
		if output.Name == previous || !canonical.IsDigest(output.BuildDigest) {
			return "", e.New(e.CodeVerifyFailed, "release content requires unique named build outputs")
		}
		output.Reference = ""
		previous = output.Name
	}
	return canonical.Digest(canonical.KindReleaseContent, content)
}

func cloneOutputs(source []Output) []Output {
	result := append([]Output(nil), source...)
	for i := range result {
		result[i].Components = append([]string(nil), source[i].Components...)
	}
	return result
}
