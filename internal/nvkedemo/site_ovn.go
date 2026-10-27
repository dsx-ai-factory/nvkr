// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"errors"
	"net/netip"
)

const siteOVNVersion = "2.4.2"
const upstreamSiteOVNVersion = "1.4.0"

// Distinct completed output names prevent a baseline Run from being replayed
// as the explicitly selected upstream diagnostic (including its baked disk).
func selectedOVNOutputs(network string) (version, image, chart string, err error) {
	mode, err := normalizeNetwork(network)
	if err != nil {
		return "", "", "", err
	}
	if mode == "upstream-ovn" {
		return upstreamSiteOVNVersion, "upstream-ovn-image-arm64", "upstream-ovn-chart-arm64", nil
	}
	if mode == "ovn" {
		return siteOVNVersion, "ovn-image-arm64", "ovn-chart-arm64", nil
	}
	return "", "", "", configUsage("selected network has no OVN outputs")
}

func selectedDiskOutput(network string) (string, error) {
	mode, err := normalizeNetwork(network)
	if err != nil {
		return "", err
	}
	if mode == "upstream-ovn" {
		return "nvke-node-image-upstream-ovn-arm64", nil
	}
	return DiskOutputName, nil
}

func selectedProfilesOutput(network string) (string, error) {
	mode, err := normalizeNetwork(network)
	if err != nil {
		return "", err
	}
	if mode == "upstream-ovn" {
		return "controller-profiles-upstream-ovn", nil
	}
	return "controller-profiles", nil
}

func admitSiteCIDRs(kind string) error {
	pod, service := netip.MustParsePrefix(managementPodCIDR), netip.MustParsePrefix(managementServiceCIDR)
	for _, value := range []string{kind, GuestCIDR, VIPCIDR} {
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix.Overlaps(pod) || prefix.Overlaps(service) {
			return errors.New("site OVN pod/service ranges overlap admitted Docker or tenant networks")
		}
	}
	return nil
}
