// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"

const (
	ObjectStoreFullMirrorMode = "full-mirror-v1"
	ObjectStoreBootMode       = "selective-boot-v1"
)

func (environment Environment) validateBootPublication() error {
	publication := environment.Publication
	if environment.APIVersion != EnvironmentBootAPIVersion {
		if publication == nil {
			return nil
		}
		if publication.BootSelection != nil {
			return e.New(e.CodeInvalidCatalog, "bootSelection requires Environment v3")
		}
		for _, destination := range publication.ObjectStores {
			if destination.Mode != "" || destination.Serving != nil {
				return e.New(e.CodeInvalidCatalog, "object-store mode and serving require Environment v3")
			}
		}
		return nil
	}
	if publication == nil || publication.BootSelection == nil {
		return e.New(e.CodeInvalidCatalog, "Environment v3 requires an explicit boot selection")
	}
	if err := publication.BootSelection.Validate(); err != nil {
		return err
	}
	if publication.Mode != "per-output-v1" || environment.Tekton != nil {
		return e.New(e.CodeEngineCapability, "selective boot publication requires the GitHub per-output route")
	}
	bootDestinations := 0
	for _, destination := range publication.ObjectStores {
		switch destination.Mode {
		case ObjectStoreFullMirrorMode:
		case ObjectStoreBootMode:
			bootDestinations++
		default:
			return e.New(e.CodeEngineCapability, "Environment v3 requires an explicit supported object-store mode")
		}
		if err := destination.Validate(); err != nil {
			return err
		}
	}
	if bootDestinations == 0 {
		return e.New(e.CodeInvalidCatalog, "boot selection requires at least one selective destination")
	}
	return nil
}
