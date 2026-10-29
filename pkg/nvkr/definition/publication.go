// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"

// validateTransports admits the complete selection before OCI-specific settings.
// An unavailable selected transport cannot be replaced by an available sibling.
func (publication Publication) validateTransports() error {
	if publication.Transports == nil {
		return nil
	}
	if len(publication.Transports) == 0 || len(publication.Transports) > 2 {
		return e.New(e.CodeInvalidCatalog, "publication transports requires a nonempty selection of apt, oci, or both")
	}
	previous := ""
	apt := false
	for _, transport := range publication.Transports {
		if (transport != PublicationTransportAPT && transport != PublicationTransportOCI) || transport <= previous {
			return e.New(e.CodeInvalidCatalog, "publication transports must be sorted, unique members of apt and oci")
		}
		apt = apt || transport == PublicationTransportAPT
		previous = transport
	}
	if apt {
		return e.New(e.CodeEngineCapability, "publication transport apt requires an APT repository publication adapter that is not implemented; every selected transport is required")
	}
	return nil
}
