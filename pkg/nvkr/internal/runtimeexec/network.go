// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package runtimeexec

import (
	"net/netip"
	"strings"
)

// The helper receives only a TAP descriptor, readiness socket, bounded DNS
// configuration and the produced root. There is no API socket or inbound port
// forwarding. Its namespace bootstrap removes every capability before exec.
func outboundCommand() []string {
	return []string{"/usr/bin/slirp4netns", "--netns-type=tapfd", "--ready-fd=7",
		"--disable-host-loopback", "--enable-seccomp", "--mtu=1500", "6"}
}

func outboundEnvironment() []string {
	return []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "LANG=C", "LC_ALL=C"}
}

func resolverConfiguration(raw []byte) string {
	if len(raw) > 64<<10 {
		return ""
	}
	var servers []string
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		address, err := netip.ParseAddr(fields[1])
		if err != nil || address.Zone() != "" || address.IsUnspecified() || address.IsMulticast() {
			return ""
		}
		// This TAP admits IPv4 only. libslirp 4.9.1's IPv4 DNS forwarding
		// resolves upstreams with AF_INET and cannot use an IPv6-only set.
		if !address.Is4() {
			continue
		}
		servers = append(servers, "nameserver "+address.String()+"\n")
		if len(servers) == 3 {
			break
		}
	}
	return strings.Join(servers, "")
}

func validResolver(value string) bool {
	return value != "" && resolverConfiguration([]byte(value)) == value
}
