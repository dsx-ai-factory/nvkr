// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package resultspublish projects qualification outcomes onto the results bucket:
// each outcome record under results/, and its TestGrid build (started.json,
// artifacts/junit.xml, finished.json) under groups/nvkr/. It writes through a
// create-only ObjectStore, so it holds no credential and links no cloud SDK; the
// publisher binary supplies the store. Every object is derived from the outcome
// alone, so a second or resumed pass writes the same bytes. Like the outcome
// itself, nothing published here grants qualification, manifest or release
// authority.
package resultspublish
