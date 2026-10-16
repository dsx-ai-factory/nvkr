// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"errors"
	"strings"
)

// nativeDemoOVNPackagePolicy projects the reviewed source fixture's package command
// onto the same OVN selection as the disk, site and tenant request. NVKE still
// owns template rendering; this exact source edit does not parse that grammar.
func nativeDemoOVNPackagePolicy(spec map[string]any, network string) error {
	version, _, _, err := selectedOVNOutputs(network)
	if err != nil {
		return err
	}
	init, ok := spec["machineInit"].(map[string]any)
	if !ok {
		return errors.New("source release omits machine initialization")
	}
	data, ok := init["userDataTemplate"].(string)
	command := "\n  - DEBIAN_FRONTEND=noninteractive apt-get install -y -o DPkg::Lock::Timeout=600 nvke-cni-ovn-images-1.4.0 nvke-cni-ovn-charts-1.4.0\n"
	sourceVersion := "1.4.0"
	if strings.Count(data, command) == 0 {
		// NVKE 69ef9924 changes both package names to 1.6.0. Admit only
		// that exact paired command, retaining rejection of unknown edits.
		sourceVersion = "1.6.0"
		command = strings.ReplaceAll(command, "1.4.0", sourceVersion)
	}
	if !ok || !strings.HasPrefix(data, "#cloud-config\n") || strings.Count(data, command) != 1 ||
		strings.Count(data, "nvke-cni-ovn-images-") != 1 || strings.Count(data, "nvke-cni-ovn-charts-") != 1 {
		return errors.New("source release OVN package command differs from the supported demo fixture")
	}
	init["userDataTemplate"] = strings.Replace(data, command, strings.ReplaceAll(command, sourceVersion, version), 1)
	return nil
}
