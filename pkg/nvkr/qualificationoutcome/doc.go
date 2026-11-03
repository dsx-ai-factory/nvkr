// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package qualificationoutcome describes the outcome of one release qualification
// attempt for publication. An outcome is an unsigned description that points at
// signed qualification evidence and release manifests by digest. It grants no
// qualification, manifest, release or publication authority, is not a lifecycle
// input, and is never digest-addressed.
//
// The package is a leaf: it imports only the canonical codec, coded errors and
// held files, so command, generator and publisher code can share it without
// linking each other.
package qualificationoutcome
