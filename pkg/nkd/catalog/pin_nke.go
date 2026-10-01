package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"path"
	"regexp"
	"sort"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"sigs.k8s.io/yaml"
)

// PinSetItem accounts for a deploy item that is not an external chart member.
// Local Helm charts additionally bind their exact source-owned relative path.
type PinSetItem struct {
	Type string `json:"type"`
	Path string `json:"path,omitempty"`
}

var nkeItemName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

type nkeDeployDocument struct {
	Items []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"items"`
}

type nkeReleaseDocument struct {
	Chart struct {
		Name       string `json:"name"`
		Version    string `json:"version"`
		Repository string `json:"repository,omitempty"`
		Path       string `json:"path,omitempty"`
	} `json:"chart"`
	Release struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"release"`
	Values   []string `json:"values,omitempty"`
	Requires []struct {
		Kind      string `json:"kind"`
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
		Condition string `json:"condition"`
	} `json:"requires,omitempty"`
}

func validateNKEPinSet(set *PinSet) error {
	if set == nil || len(set.Members) == 0 || len(set.Members)+len(set.Accounted) > maxPinSetMembers {
		return e.New(e.CodeInvalidCatalog, "NKE pin set requires members and at most %d total declared items", maxPinSetMembers)
	}
	if err := validatePinSetFilePath(set.File); err != nil {
		return err
	}
	for _, name := range pinSetMemberNames(set) {
		member := set.Members[name]
		if !nkeItemName.MatchString(name) || len(name) > 253 || member.Path != "" {
			return e.New(e.CodeInvalidCatalog, "NKE pin member %q requires a plain deploy item name and no value path", name)
		}
		if _, exists := set.Accounted[name]; exists {
			return e.New(e.CodeInvalidCatalog, "NKE item %q cannot be both a member and accounted", name)
		}
		if (member.Source == "") == (member.Artifact == "") {
			return e.New(e.CodeInvalidCatalog, "NKE member %q requires exactly one source or artifact", name)
		}
		if member.Source != "" {
			if member.Chart == nil {
				return e.New(e.CodeInvalidCatalog, "NKE source member %q requires explicit chart identity", name)
			}
			if err := validateHelmChart(*member.Chart); err != nil {
				return err
			}
		} else if member.Chart != nil {
			return e.New(e.CodeInvalidCatalog, "NKE artifact member %q takes chart identity from its acquired producer", name)
		}
	}
	for _, name := range nkeAccountedNames(set.Accounted) {
		item := set.Accounted[name]
		if !nkeItemName.MatchString(name) || len(name) > 253 {
			return e.New(e.CodeInvalidCatalog, "invalid accounted NKE item name %q", name)
		}
		switch item.Type {
		case "helm":
			if _, err := nkeLocalChartFile(set.File, item.Path); err != nil {
				return err
			}
		case "manifest", "template", "argocd":
			if item.Path != "" {
				return e.New(e.CodeInvalidCatalog, "non-Helm NKE item %q cannot declare a local chart path", name)
			}
		default:
			return e.New(e.CodeInvalidCatalog, "unsupported NKE item type %q for %q", item.Type, name)
		}
	}
	return nil
}

func deriveNKEPinSet(ctx context.Context, tree pinnedTree, reg *registryDoc, source string) (map[string]string, error) {
	if err := pinContextError(ctx); err != nil {
		return nil, err
	}
	set := reg.Sources[source].PinSet
	if err := validateNKEPinSet(set); err != nil {
		return nil, err
	}
	raw, err := tree.ReadFile(ctx, set.File)
	if err != nil {
		return nil, err
	}
	var deploy nkeDeployDocument
	if err := decodeNKEPinDocument(raw, &deploy); err != nil {
		return nil, err
	}
	if len(deploy.Items) == 0 || len(deploy.Items) > maxPinSetMembers {
		return nil, e.New(e.CodeInvalidCatalog, "NKE deploy path requires 1..%d items", maxPinSetMembers)
	}
	seen := make(map[string]bool, len(deploy.Items))
	// Admit exact membership and kinds before following any item file reference.
	for _, item := range deploy.Items {
		if !nkeItemName.MatchString(item.Name) || len(item.Name) > 253 || seen[item.Name] {
			return nil, e.New(e.CodeInvalidCatalog, "invalid or duplicate NKE deploy item %q", item.Name)
		}
		seen[item.Name] = true
		if _, member := set.Members[item.Name]; member {
			if item.Type != "helm" {
				return nil, e.New(e.CodeInvalidCatalog, "NKE member %q changed type from helm to %q", item.Name, item.Type)
			}
			continue
		}
		accounted, ok := set.Accounted[item.Name]
		if !ok || accounted.Type != item.Type {
			return nil, e.New(e.CodeInvalidCatalog, "unknown or reclassified NKE deploy item %q (%s); account for it explicitly", item.Name, item.Type)
		}
	}
	for _, name := range pinSetMemberNames(set) {
		if !seen[name] {
			return nil, e.New(e.CodeInvalidCatalog, "NKE deploy path is missing declared member %q", name)
		}
	}
	for _, name := range nkeAccountedNames(set.Accounted) {
		if !seen[name] {
			return nil, e.New(e.CodeInvalidCatalog, "NKE deploy path is missing accounted item %q", name)
		}
	}
	versions := make(map[string]string, len(set.Members))
	for _, item := range deploy.Items {
		if item.Type != "helm" {
			continue
		}
		raw, err := tree.ReadFile(ctx, path.Join(path.Dir(set.File), item.Name, "release.yaml"))
		if err != nil {
			return nil, err
		}
		var release nkeReleaseDocument
		if err := decodeNKEPinDocument(raw, &release); err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "NKE Helm item %q", item.Name)
		}
		if !helmChartName.MatchString(release.Chart.Name) || !exactPinSetVersion(release.Chart.Version) ||
			release.Release.Name == "" || release.Release.Namespace == "" ||
			(release.Chart.Path == "") == (release.Chart.Repository == "") {
			return nil, e.New(e.CodeInvalidCatalog, "NKE Helm item %q requires chart name, exact version, release name/namespace, and exactly one repository or path", item.Name)
		}
		member, external := set.Members[item.Name]
		if external {
			if err := validateNKEExternalChart(reg, item.Name, member, release); err != nil {
				return nil, err
			}
			versions[item.Name] = release.Chart.Version
			continue
		}
		if err := validateNKELocalChart(ctx, tree, set.File, item.Name, set.Accounted[item.Name], release); err != nil {
			return nil, err
		}
	}
	return versions, nil
}

func validateNKEExternalChart(reg *registryDoc, item string, member PinSetMember, release nkeReleaseDocument) error {
	want := member.Chart
	if member.Artifact != "" {
		producer, ok := reg.Components[member.Artifact]
		if !ok || producer.Prebuilt == nil {
			return e.New(e.CodeInvalidCatalog, "NKE member %q requires a declared acquired chart producer", item)
		}
		want = producer.Prebuilt.Chart
	}
	if want == nil || release.Chart.Repository == "" || release.Chart.Path != "" ||
		want.Name != release.Chart.Name || want.Repository != release.Chart.Repository {
		return e.New(e.CodeInvalidCatalog, "NKE member %q chart name/repository differs from its reviewed acquisition identity", item)
	}
	return validateHelmChart(*want)
}

func validateNKELocalChart(ctx context.Context, tree pinnedTree, deployFile, item string, accounted PinSetItem, release nkeReleaseDocument) error {
	if release.Chart.Path == "" || release.Chart.Repository != "" || accounted.Path != release.Chart.Path {
		return e.New(e.CodeInvalidCatalog, "accounted NKE Helm item %q must retain its declared local chart path", item)
	}
	chartFile, err := nkeLocalChartFile(deployFile, release.Chart.Path)
	if err != nil {
		return err
	}
	raw, err := tree.ReadFile(ctx, chartFile)
	if err != nil {
		return err
	}
	root, err := decodePinSetDocument(raw)
	if err != nil {
		return err
	}
	chartName := pinSetObjectField(root, "name")
	chartVersion := pinSetObjectField(root, "version")
	if chartName == nil || chartName.Tag != "!!str" || chartName.Value != release.Chart.Name ||
		chartVersion == nil || chartVersion.Tag != "!!str" || chartVersion.Value != release.Chart.Version {
		return e.New(e.CodeInvalidCatalog, "NKE local chart %q name/version differs from release.yaml", item)
	}
	return nil
}

func nkeLocalChartFile(deployFile, relative string) (string, error) {
	if relative == "" || path.IsAbs(relative) || path.Clean(relative) != relative || strings.ContainsAny(relative, "\\:") {
		return "", e.New(e.CodeInvalidCatalog, "NKE local chart path %q must be a canonical relative path", relative)
	}
	resolved := path.Join(path.Dir(deployFile), relative, "Chart.yaml")
	if err := validatePinSetFilePath(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

func decodeNKEPinDocument(raw []byte, target any) error {
	if _, err := decodePinSetDocument(raw); err != nil {
		return err
	}
	encoded, err := yaml.YAMLToJSONStrict(raw)
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "invalid NKE deploy document")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "unsupported NKE deploy document shape")
	}
	return nil
}

func nkeAccountedNames(items map[string]PinSetItem) []string {
	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
