// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

// GeneratorLinkerFlags returns the validated Go linker flags that reproduce a
// Plan's generator version label. Adapters use the same parser as Run rendering;
// this descriptive label grants no build or release authority.
func GeneratorLinkerFlags(version string) (string, error) {
	return generatorLinkerFlags(version)
}
