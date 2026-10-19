// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import authored "github.com/dsx-ai-factory/nvkr/pkg/nvkr/definition"

const fixtureUtilityContainer = "golang@sha256:fe5d57d3b718e7a4986bae156c2d73f44973bfd313073aed08a4de6692bb6161"

func fixtureInputs(name, output, runsOn string) (authored.Definition, authored.Environment) {
	// Two independent producers exercise ordering and per-component joins without
	// rebuilding the entire Kubernetes selection for every factory contract.
	return authored.Definition{APIVersion: authored.DefinitionAPIVersion, Kind: authored.DefinitionKind,
			Metadata: authored.Metadata{Name: name}, Catalog: authored.Catalog{Base: "testdata/catalogs/kubernetes-layered"},
			Outputs: []authored.DefinitionOutput{{Name: output, Platform: "linux/amd64", Format: authored.OutputExecutable, Components: []string{"containerd", "kubelet"}}}},
		authored.Environment{APIVersion: authored.EnvironmentAPIVersion, Kind: authored.EnvironmentKind, Metadata: authored.Metadata{Name: "fixture"}, RunsOn: runsOn, UtilityContainer: fixtureUtilityContainer, UtilityPlatform: "linux/amd64"}
}
