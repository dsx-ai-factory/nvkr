// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/google/go-containerregistry/pkg/name"
	"go.yaml.in/yaml/v3"
)

// This private selection retains descriptive source/version agreement after the
// existing owners verify the complete Run and payload. It grants no Release or
// producer authority and cannot be supplied through the public asset config.
type nativeInstallerSelection struct {
	repo, commit, capiVersion, versionsDigest, siteSpecDigest string
	sourceDate                                                int64
}

func nativeInstallerSelectionFromRun(ctx context.Context, root, bundle string) (*nativeInstallerSelection, error) {
	plan, output, err := nativeSelectedRunPlan(ctx, root, bundle, "executable")
	if err != nil {
		return nil, err
	}
	return nativeInstallerSelectionFromPlan(plan, bundle, output.BuildDigest)
}

func nativeInstallerSelectionFromPlan(plan *factory.Plan, bundle, buildDigest string) (*nativeInstallerSelection, error) {
	for _, build := range plan.Builds {
		if build.Name != bundle {
			continue
		}
		if build.BuildDigest != buildDigest {
			return nil, errors.New("native tool output differs from its selected source build")
		}
		components := make(map[string]catalog.Component, len(build.ResolvedComponents))
		for _, item := range build.ResolvedComponents {
			components[item.Name] = item.Component
		}
		installer, ok := components["nvke-installer-sources"]
		if !ok || !installer.FromSource || installer.Commit == "" || len(installer.Patches) != 0 {
			return nil, errors.New("native tool Run omits the unmodified source-owned installer")
		}
		for _, name := range []string{"nvkectl", "nico-sim", "nico-simctl"} {
			component, ok := components[name]
			if !ok || !component.FromSource || component.Repo != installer.Repo || component.Commit != installer.Commit || component.TreeHash != installer.TreeHash {
				return nil, errors.New("native installer and executable source selections differ")
			}
		}
		var version string
		for _, name := range []string{"clusterctl", "clusterctl-linux"} {
			if component, ok := components[name]; ok {
				if version != "" {
					return nil, errors.New("native tool Run selects ambiguous clusterctl versions")
				}
				version = component.Tag
			}
		}
		parsed, err := semver.StrictNewVersion(strings.TrimPrefix(version, "v"))
		if err != nil || version != "v"+parsed.String() || parsed.Prerelease() != "" || parsed.Metadata() != "" {
			return nil, errors.New("native tool Run requires an exact stable CAPI provider version")
		}
		return &nativeInstallerSelection{repo: installer.Repo, commit: installer.Commit, sourceDate: installer.SourceDate, capiVersion: version}, nil
	}
	return nil, errors.New("native tool Run omits the selected host bundle build")
}

const nativeSiteSpecPath = "cli/internal/bootstrap/sitespec/template.yaml"

// NVKE's manifest release owns the tag its unchanged deployment expects. This
// projection selects only the local import alias; NVKE still validates and
// installs its complete release declaration, and delivery verifies image bytes.
func nativeCAPNICoReference(ctx context.Context, installer string) (string, error) {
	raw, err := readRunFile(ctx, installer, "deploy/nico/capnico/release.yaml", 64<<10)
	if err != nil {
		return "", err
	}
	var release struct{ Image, Artifact string }
	if err := yaml.Unmarshal(raw, &release); err != nil {
		return "", err
	}
	ref, err := name.NewTag(release.Image, name.StrictValidation)
	if err != nil || release.Artifact != "capnico-controller" || !nativeImageReferenceIsCanonical(ref, release.Image) {
		return "", errors.New("CAPNICo import requires the source-owned manifest image and artifact")
	}
	return release.Image, nil
}

// NVKE owns the full site-spec schema and validates the rendered specification.
// This projection reads only its selected source default. It is checked against
// the already verified installer payload at each native execution boundary.
func nativeKubernetesVersion(ctx context.Context, assets NativeAssets) (string, error) {
	if _, err := nativeCAPIProviderVersion(ctx, assets); err != nil {
		return "", err
	}
	raw, err := readRunFile(ctx, assets.Installer, nativeSiteSpecPath, 64<<10)
	if err != nil {
		return "", fmt.Errorf("read source-owned Kubernetes selection: %w", err)
	}
	if assets.installerSelection.siteSpecDigest == "" || plainSHA(raw) != assets.installerSelection.siteSpecDigest {
		return "", errors.New("native site specification differs from the checked installer payload")
	}
	var spec struct {
		Cluster struct {
			Kubernetes string `yaml:"k8sVersion"`
		} `yaml:"cluster"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		return "", fmt.Errorf("read NVKE site-spec version projection: %w", err)
	}
	if !stableKubernetesVersion(spec.Cluster.Kubernetes) {
		return "", errors.New("NVKE site specification requires an exact stable Kubernetes version")
	}
	return spec.Cluster.Kubernetes, nil
}

func stableKubernetesVersion(version string) bool {
	parsed, err := semver.StrictNewVersion(version)
	return err == nil && version == parsed.String() && parsed.Prerelease() == "" && parsed.Metadata() == ""
}
