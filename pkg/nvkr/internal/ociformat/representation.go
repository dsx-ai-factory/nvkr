// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package ociformat owns the explicit construction choices shared by authored
// publication configuration and the delivery constructors. It performs no I/O.
package ociformat

type Representation string

const (
	ArtifactV1       Representation = "artifact-v1"
	NVKRCompatibleV1 Representation = "nvkr-compatible-v1"
	HelmStandardV1   Representation = "helm-standard-v1"
)

func Valid(value Representation) bool {
	return value == "" || value == ArtifactV1 || value == NVKRCompatibleV1 || value == HelmStandardV1
}
