// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package definition

import (
	"io/fs"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const BootServingProfile = "https-content-addressed-v1"

// BootServing declares the authorized client path independently of the S3 API
// endpoint. Prefix maps the store-relative immutable key below an HTTPS origin;
// it is a literal absolute path, not a URL template or a query encoding.
// Declaration alone proves neither retrieval nor durable route/retention policy.
type BootServing struct {
	Profile       string `json:"profile"`
	Origin        string `json:"origin"`
	Prefix        string `json:"prefix"`
	ReaderMode    string `json:"readerMode"`
	CredentialRef string `json:"credentialRef,omitempty"`
}

func (serving BootServing) Validate() error {
	if serving.Profile != BootServingProfile {
		return e.New(e.CodeEngineCapability, "unsupported boot-serving profile %q", serving.Profile)
	}
	origin, err := url.Parse(serving.Origin)
	if err != nil || origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil ||
		origin.Path != "" || origin.Opaque != "" || strings.ContainsAny(serving.Origin, "%?#\\") ||
		len(serving.Origin) > 512 || strings.ContainsFunc(serving.Origin, func(c rune) bool { return c <= ' ' || c > '~' }) ||
		origin.String() != serving.Origin || strings.ToLower(origin.Host) != origin.Host {
		return e.New(e.CodeInvalidCatalog, "boot serving requires a canonical HTTPS origin without credentials, path, query or fragment")
	}
	if strings.HasSuffix(origin.Host, ":") {
		return e.New(e.CodeInvalidCatalog, "boot serving origin has an empty port")
	}
	if !bootServingHost(origin.Hostname()) {
		return e.New(e.CodeInvalidCatalog, "boot serving origin requires a canonical DNS hostname or IP literal")
	}
	if strings.HasPrefix(origin.Host, "[") {
		address, err := netip.ParseAddr(origin.Hostname())
		if err != nil || !address.Is6() {
			return e.New(e.CodeInvalidCatalog, "boot serving origin brackets require an IPv6 literal")
		}
	}
	if port := origin.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value <= 0 || value > 65535 || value == 443 || strconv.Itoa(value) != port {
			return e.New(e.CodeInvalidCatalog, "boot serving origin has a noncanonical port")
		}
	}
	prefix := strings.TrimPrefix(serving.Prefix, "/")
	if len(serving.Prefix) > 512 || !strings.HasPrefix(serving.Prefix, "/") ||
		(serving.Prefix != "/" && (prefix == "." || !fs.ValidPath(prefix))) ||
		strings.ContainsFunc(serving.Prefix, func(c rune) bool {
			return c != '/' && c != '-' && c != '_' && c != '.' && c != '~' &&
				(c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9')
		}) {
		return e.New(e.CodeInvalidCatalog, "boot serving prefix must be a literal absolute path of unreserved URL characters")
	}
	switch serving.ReaderMode {
	case "anonymous":
		if serving.CredentialRef != "" {
			return e.New(e.CodeTrustPolicyMissing, "anonymous boot serving cannot carry a credential reference")
		}
	case "bearer":
		if !githubCredentialRefPattern.MatchString(serving.CredentialRef) || strings.HasPrefix(strings.ToUpper(strings.TrimPrefix(serving.CredentialRef, "github:")), "GITHUB_") {
			return e.New(e.CodeTrustPolicyMissing, "boot serving bearer access requires an explicit github credential reference")
		}
	default:
		return e.New(e.CodeEngineCapability, "boot serving requires an explicit admitted reader mode")
	}
	return nil
}

func bootServingHost(host string) bool {
	if address, err := netip.ParseAddr(host); err == nil {
		return address.Zone() == "" && address.String() == host
	}
	if len(host) == 0 || len(host) > 253 || strings.Trim(host, "0123456789.") == "" {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		if strings.ContainsFunc(label, func(c rune) bool { return c != '-' && (c < 'a' || c > 'z') && (c < '0' || c > '9') }) {
			return false
		}
	}
	return true
}
