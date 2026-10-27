// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"path"
	"regexp"
	"sort"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

// PinSetItem accounts for a deploy item that is not an external chart member.
// Local Helm charts additionally bind their exact source-owned relative path.
type PinSetItem struct {
	Type      string `json:"type"`
	Path      string `json:"path,omitempty"`
	ChartName string `json:"chartName,omitempty"`
}

var nvkeItemName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

type nvkeDeployDocument struct {
	Items []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"items"`
}

type nvkeReleaseDocument struct {
	Chart struct {
		Name             string           `json:"name"`
		Version          nvkeChartVersion `json:"version"`
		Repository       string           `json:"repository,omitempty"`
		Path             string           `json:"path,omitempty"`
		Artifact         nvkeArtifactName `json:"artifact,omitempty"`
		ImageArtifact    nvkeArtifactName `json:"imageArtifact,omitempty"`
		ProfilesArtifact nvkeArtifactName `json:"profilesArtifact,omitempty"`
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

// An omitted local version defers to its pinned Chart.yaml. A supplied version
// must still be exact; empty, null and non-string values are not omission.
type nvkeChartVersion string

func (version *nvkeChartVersion) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if !exactPinSetVersion(value) {
		return e.New(e.CodeInvalidCatalog, "NVKE chart version must be an exact semantic-version string")
	}
	*version = nvkeChartVersion(value)
	return nil
}

// Consumer artifact mappings describe names only. Parsing them neither selects
// catalog producers nor authorizes output membership in a Release.
type nvkeArtifactName string

func (name *nvkeArtifactName) UnmarshalJSON(raw []byte) error {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	if !nvkeItemName.MatchString(value) || len(value) > 253 {
		return e.New(e.CodeInvalidCatalog, "NVKE chart artifact mapping must be a plain name")
	}
	*name = nvkeArtifactName(value)
	return nil
}

func validateNVKEPinSet(set *PinSet) error {
	if set == nil || len(set.Members) == 0 || len(set.Members)+len(set.Accounted) > maxPinSetMembers {
		return e.New(e.CodeInvalidCatalog, "NVKE pin set requires members and at most %d total declared items", maxPinSetMembers)
	}
	if err := validatePinSetFilePath(set.File); err != nil {
		return err
	}
	for _, name := range pinSetMemberNames(set) {
		member := set.Members[name]
		if !nvkeItemName.MatchString(name) || len(name) > 253 || member.Path != "" {
			return e.New(e.CodeInvalidCatalog, "NVKE pin member %q requires a plain deploy item name and no value path", name)
		}
		if _, exists := set.Accounted[name]; exists {
			return e.New(e.CodeInvalidCatalog, "NVKE item %q cannot be both a member and accounted", name)
		}
		if (member.Source == "") == (member.Artifact == "") {
			return e.New(e.CodeInvalidCatalog, "NVKE member %q requires exactly one source or artifact", name)
		}
		if member.Source != "" {
			if member.Chart == nil {
				return e.New(e.CodeInvalidCatalog, "NVKE source member %q requires explicit chart identity", name)
			}
			if err := validateHelmChart(*member.Chart); err != nil {
				return err
			}
		} else if member.Chart != nil {
			return e.New(e.CodeInvalidCatalog, "NVKE artifact member %q takes chart identity from its acquired producer", name)
		}
	}
	for _, name := range nvkeAccountedNames(set.Accounted) {
		item := set.Accounted[name]
		if !nvkeItemName.MatchString(name) || len(name) > 253 {
			return e.New(e.CodeInvalidCatalog, "invalid accounted NVKE item name %q", name)
		}
		switch item.Type {
		case "helm":
			if item.ChartName != "" && !helmChartName.MatchString(item.ChartName) {
				return e.New(e.CodeInvalidCatalog, "invalid accounted local chart name %q", item.ChartName)
			}
			if _, err := nvkeLocalChartFile(set.File, item.Path); err != nil {
				return err
			}
		case "manifest", "template", "argocd":
			if item.Path != "" || item.ChartName != "" {
				return e.New(e.CodeInvalidCatalog, "non-Helm NVKE item %q cannot declare local chart metadata", name)
			}
		default:
			return e.New(e.CodeInvalidCatalog, "unsupported NVKE item type %q for %q", item.Type, name)
		}
	}
	return nil
}

func deriveNVKEPinSet(ctx context.Context, tree pinnedTree, reg *registryDoc, source string) (map[string]string, error) {
	if err := pinContextError(ctx); err != nil {
		return nil, err
	}
	set := reg.Sources[source].PinSet
	if err := validateNVKEPinSet(set); err != nil {
		return nil, err
	}
	raw, err := tree.ReadFile(ctx, set.File)
	if err != nil {
		return nil, err
	}
	var deploy nvkeDeployDocument
	if err := decodeNVKEPinDocument(raw, &deploy); err != nil {
		return nil, err
	}
	if len(deploy.Items) == 0 || len(deploy.Items) > maxPinSetMembers {
		return nil, e.New(e.CodeInvalidCatalog, "NVKE deploy path requires 1..%d items", maxPinSetMembers)
	}
	seen := make(map[string]bool, len(deploy.Items))
	// Admit exact membership and kinds before following any item file reference.
	for _, item := range deploy.Items {
		if !nvkeItemName.MatchString(item.Name) || len(item.Name) > 253 || seen[item.Name] {
			return nil, e.New(e.CodeInvalidCatalog, "invalid or duplicate NVKE deploy item %q", item.Name)
		}
		seen[item.Name] = true
		if _, member := set.Members[item.Name]; member {
			if item.Type != "helm" {
				return nil, e.New(e.CodeInvalidCatalog, "NVKE member %q changed type from helm to %q", item.Name, item.Type)
			}
			continue
		}
		accounted, ok := set.Accounted[item.Name]
		if !ok || accounted.Type != item.Type {
			return nil, e.New(e.CodeInvalidCatalog, "unknown or reclassified NVKE deploy item %q (%s); account for it explicitly", item.Name, item.Type)
		}
	}
	for _, name := range pinSetMemberNames(set) {
		if !seen[name] {
			return nil, e.New(e.CodeInvalidCatalog, "NVKE deploy path is missing declared member %q", name)
		}
	}
	for _, name := range nvkeAccountedNames(set.Accounted) {
		if !seen[name] {
			return nil, e.New(e.CodeInvalidCatalog, "NVKE deploy path is missing accounted item %q", name)
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
		var release nvkeReleaseDocument
		if err := decodeNVKEPinDocument(raw, &release); err != nil {
			return nil, e.Wrap(e.CodeInvalidCatalog, err, "NVKE Helm item %q", item.Name)
		}
		if !helmChartName.MatchString(release.Chart.Name) ||
			release.Release.Name == "" || release.Release.Namespace == "" ||
			(release.Chart.Path == "") == (release.Chart.Repository == "") {
			return nil, e.New(e.CodeInvalidCatalog, "NVKE Helm item %q requires chart name, release name/namespace, and exactly one repository or path", item.Name)
		}
		member, external := set.Members[item.Name]
		if external {
			if err := validateNVKEExternalChart(reg, item.Name, member, release); err != nil {
				return nil, err
			}
			versions[item.Name] = string(release.Chart.Version)
			continue
		}
		if err := validateNVKELocalChart(ctx, tree, set.File, item.Name, set.Accounted[item.Name], release); err != nil {
			return nil, err
		}
	}
	return versions, nil
}

func validateNVKEExternalChart(reg *registryDoc, item string, member PinSetMember, release nvkeReleaseDocument) error {
	want := member.Chart
	if member.Artifact != "" {
		producer, ok := reg.Components[member.Artifact]
		if !ok || producer.Prebuilt == nil {
			return e.New(e.CodeInvalidCatalog, "NVKE member %q requires a declared acquired chart producer", item)
		}
		want = producer.Prebuilt.Chart
	}
	if !exactPinSetVersion(string(release.Chart.Version)) {
		return e.New(e.CodeInvalidCatalog, "NVKE external Helm item %q requires an exact chart version", item)
	}
	if release.Chart.Artifact != "" || release.Chart.ImageArtifact != "" || release.Chart.ProfilesArtifact != "" {
		return e.New(e.CodeInvalidCatalog, "NVKE external Helm item %q cannot declare local artifact mappings", item)
	}
	if want == nil || release.Chart.Repository == "" || release.Chart.Path != "" ||
		want.Name != release.Chart.Name || want.Repository != release.Chart.Repository {
		return e.New(e.CodeInvalidCatalog, "NVKE member %q chart name/repository differs from its reviewed acquisition identity", item)
	}
	return validateHelmChart(*want)
}

func validateNVKELocalChart(ctx context.Context, tree pinnedTree, deployFile, item string, accounted PinSetItem, release nvkeReleaseDocument) error {
	if release.Chart.Path == "" || release.Chart.Repository != "" || accounted.Path != release.Chart.Path {
		return e.New(e.CodeInvalidCatalog, "accounted NVKE Helm item %q must retain its declared local chart path", item)
	}
	chartFile, err := nvkeLocalChartFile(deployFile, release.Chart.Path)
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
	expectedName := release.Chart.Name
	if accounted.ChartName != "" {
		expectedName = accounted.ChartName
	}
	chartName := pinSetObjectField(root, "name")
	chartVersion := pinSetObjectField(root, "version")
	if chartName == nil || chartName.Tag != "!!str" || !helmChartName.MatchString(chartName.Value) || len(chartName.Value) > 253 || chartName.Value != expectedName ||
		chartVersion == nil || chartVersion.Tag != "!!str" || !exactPinSetVersion(chartVersion.Value) ||
		(release.Chart.Version != "" && chartVersion.Value != string(release.Chart.Version)) {
		return e.New(e.CodeInvalidCatalog, "NVKE local chart %q name/version differs from release.yaml", item)
	}
	return nil
}

func nvkeLocalChartFile(deployFile, relative string) (string, error) {
	if relative == "" || path.IsAbs(relative) || path.Clean(relative) != relative || strings.ContainsAny(relative, "\\:") {
		return "", e.New(e.CodeInvalidCatalog, "NVKE local chart path %q must be a canonical relative path", relative)
	}
	resolved := path.Join(path.Dir(deployFile), relative, "Chart.yaml")
	if err := validatePinSetFilePath(resolved); err != nil {
		return "", err
	}
	return resolved, nil
}

func decodeNVKEPinDocument(raw []byte, target any) error {
	if _, err := decodePinSetDocument(raw); err != nil {
		return err
	}
	encoded, err := yaml.YAMLToJSONStrict(raw)
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "invalid NVKE deploy document")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "unsupported NVKE deploy document shape")
	}
	return nil
}

func nvkeAccountedNames(items map[string]PinSetItem) []string {
	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
