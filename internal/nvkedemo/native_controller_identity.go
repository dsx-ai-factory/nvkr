// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"io"

	"github.com/dsx-ai-factory/nvkr/internal/documentpath"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"go.yaml.in/yaml/v3"
)

// These source layouts explicitly align release, namespace, Deployment,
// controller container and selector name. Do not implement Helm's general name
// or values-merging rules here: other layouts need a source/render contract.
type nativeControllerIdentity struct {
	name    string
	digests [4]string
}

var nativeControllerSourceFiles = [4]string{
	"deploy/nico/controller/release.yaml",
	"deploy/nico/controller/values.yaml",
	"deploy/nico/controller/overlay.yaml",
	"controller/deploy/charts/controller/Chart.yaml",
}

func (identity nativeControllerIdentity) admit() error {
	if identity.name != "nke-controller" && identity.name != "nvke-controller" {
		return e.New(e.CodeEngineCapability, "native site requires a supported source-selected controller identity")
	}
	return nil
}

func selectNativeControllerIdentity(ctx context.Context, installer string) (identity nativeControllerIdentity, result error) {
	defer func() {
		if result != nil {
			identity = nativeControllerIdentity{}
			result = e.Wrap(e.CodeEngineCapability, result, "admit source-selected native controller identity")
		}
	}()
	var release struct {
		Chart struct {
			Name, Path, Artifact string
			ImageArtifact        string `yaml:"imageArtifact"`
			ProfilesArtifact     string `yaml:"profilesArtifact"`
		} `yaml:"chart"`
		Release struct{ Name, Namespace string } `yaml:"release"`
		Values  []string                         `yaml:"values"`
	}
	var base, overlay struct {
		Name      yaml.Node `yaml:"nameOverride"`
		Fullname  yaml.Node `yaml:"fullnameOverride"`
		Namespace yaml.Node `yaml:"namespaceOverride"`
	}
	var chart struct {
		Name string `yaml:"name"`
	}
	for index, destination := range []any{&release, &base, &overlay, &chart} {
		raw, err := readRunFile(ctx, installer, nativeControllerSourceFiles[index], 256<<10)
		if err != nil {
			return identity, err
		}
		if index == 1 || index == 2 {
			if err := decodeNativeControllerOverrides(raw, destination); err != nil {
				return identity, err
			}
		} else {
			document, err := documentpath.Decode(raw)
			if err != nil {
				return identity, err
			}
			if err := document.Decode(destination); err != nil {
				return identity, err
			}
		}
		identity.digests[index] = plainSHA(raw)
	}
	identity.name = release.Release.Name
	if err := identity.admit(); err != nil {
		return nativeControllerIdentity{}, err
	}
	// Both admitted sources already publish the chart as nvke-controller. The
	// retained 400c release still declares the old runtime name explicitly.
	if release.Release.Namespace != identity.name || release.Chart.Name != identity.name || chart.Name != "nvke-controller" ||
		release.Chart.Path != "../../controller/deploy/charts/controller" || release.Chart.Artifact != "controller-chart" ||
		release.Chart.ImageArtifact != "cwe" || release.Chart.ProfilesArtifact != "controller-profiles" ||
		len(release.Values) != 2 || release.Values[0] != "values.yaml" || release.Values[1] != "overlay.yaml" ||
		!nativeControllerOverride(base.Namespace, identity.name) || base.Name.Kind != 0 || base.Fullname.Kind != 0 ||
		!nativeControllerOverride(overlay.Name, identity.name) || !nativeControllerOverride(overlay.Fullname, identity.name) || !nativeControllerOverride(overlay.Namespace, identity.name) {
		return nativeControllerIdentity{}, e.New(e.CodeEngineCapability, "native controller release, chart and explicit naming overrides disagree or use an unsupported layout")
	}
	return identity, nil
}

// The source overlay repeats unrelated env keys, whose interpretation remains
// NVKE/Helm-owned. Project only these explicit naming declarations through the
// YAML codec. Decode still rejects duplicate naming keys; nulls remain nodes and
// cannot silently inherit a Helm default. No values are merged here.
func decodeNativeControllerOverrides(raw []byte, destination any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return err
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return e.New(e.CodeEngineCapability, "controller naming values require one YAML object")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return e.New(e.CodeEngineCapability, "controller naming values require exactly one YAML document")
	}
	selected := yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	fields := document.Content[0].Content
	for index := 0; index < len(fields); index += 2 {
		key := fields[index]
		if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
			return e.New(e.CodeEngineCapability, "controller naming values require literal string keys")
		}
		switch key.Value {
		case "nameOverride", "fullnameOverride", "namespaceOverride":
			selected.Content = append(selected.Content, key, fields[index+1])
		case "<<":
			return e.New(e.CodeEngineCapability, "controller naming values cannot inherit a YAML merge")
		}
	}
	return selected.Decode(destination)
}

func nativeControllerOverride(node yaml.Node, expected string) bool {
	return node.Kind == yaml.ScalarNode && node.Tag == "!!str" && node.Anchor == "" && node.Value == expected
}

func nativeControllerForAssets(ctx context.Context, assets NativeAssets) (nativeControllerIdentity, error) {
	if assets.installerSelection == nil {
		return nativeControllerIdentity{}, e.New(e.CodeEngineCapability, "native controller requires its checked installer selection")
	}
	selected := assets.installerSelection.controller
	if err := selected.admit(); err != nil {
		return nativeControllerIdentity{}, err
	}
	observed, err := selectNativeControllerIdentity(ctx, assets.Installer)
	if err != nil {
		return nativeControllerIdentity{}, err
	}
	if observed != selected {
		return nativeControllerIdentity{}, e.New(e.CodeEngineCapability, "native controller metadata differs from its checked installer payload")
	}
	return selected, nil
}
