// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"encoding/json"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func evaluateNativeSmokeMember(member ToolSmokeMember, observed nativeSmokeObservation) (ToolSmokeMember, error) {
	if observed == nil {
		return member, e.New(e.CodeGateUnmet, "smoke runtime issued no observation")
	}
	profile, err := nativeSmokeProfile(member.Profile)
	if err != nil {
		return member, err
	}
	digest, size := observed.Subject()
	if observed.Profile() != profile || digest != member.PayloadDigest || size != member.Size || observed.Platform() != member.Platform || !canonical.IsDigest(observed.Image()) || !canonical.IsDigest(observed.RuntimeIdentity()) {
		return member, e.New(e.CodeVerifyFailed, "smoke observation belongs to different bytes, profile, platform or runtime")
	}
	raw := observed.Stdout()
	if len(raw) == 0 || len(raw) > pipeline.MaxToolSmokeOutputBytes || len(observed.Stderr()) != 0 {
		return member, e.New(e.CodeVerifyFailed, "smoke version command returned missing, oversized or diagnostic output")
	}
	switch member.Profile {
	case "helm-version", "kind-version":
		member.ObservedVersion = strings.TrimSuffix(string(raw), "\n")
	case "clusterctl-version-json":
		var version struct {
			Client struct {
				Version  string `json:"gitVersion"`
				Commit   string `json:"gitCommit"`
				Tree     string `json:"gitTreeState"`
				Platform string `json:"platform"`
			} `json:"clusterctl"`
		}
		if err := decodeSmokeVersion(raw, &version); err != nil {
			return member, err
		}
		if version.Client.Tree != "clean" || version.Client.Platform != member.Platform {
			return member, e.New(e.CodeVerifyFailed, "clusterctl reports a dirty build or different platform")
		}
		member.ObservedVersion, member.ObservedCommit = version.Client.Version, version.Client.Commit
	case "oras-version":
		fields := map[string]string{}
		for _, line := range strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n") {
			key, value, ok := strings.Cut(line, ":")
			if !ok || fields[key] != "" {
				return member, e.New(e.CodeVerifyFailed, "ambiguous ORAS version output")
			}
			value = strings.TrimSpace(value)
			switch key {
			case "Version", "Go version", "OS/Arch", "Git commit", "Git tree state":
			default:
				return member, e.New(e.CodeVerifyFailed, "unknown ORAS version field")
			}
			if value == "" {
				return member, e.New(e.CodeVerifyFailed, "empty ORAS version field")
			}
			fields[key] = value
		}
		if len(fields) != 5 || fields["OS/Arch"] != member.Platform || fields["Git tree state"] != "clean" {
			return member, e.New(e.CodeVerifyFailed, "ORAS reports incomplete metadata, a dirty build or different platform")
		}
		member.ObservedVersion, member.ObservedCommit = fields["Version"], fields["Git commit"]
	case "nvkectl-version-json", "nicosim-version-json", "nicosim-patched-version-json":
		var version struct {
			Version  string `json:"buildVersion"`
			Commit   string `json:"gitCommit"`
			Tree     string `json:"gitTreeState"`
			Platform string `json:"platform"`
		}
		if err := decodeSmokeVersion(raw, &version); err != nil {
			return member, err
		}
		tree := "clean"
		if member.Profile == "nicosim-patched-version-json" {
			tree = "patched"
		}
		if version.Tree != tree || version.Platform != member.Platform {
			return member, e.New(e.CodeVerifyFailed, "tool reports an unexpected source-tree state or different platform")
		}
		member.ObservedVersion, member.ObservedCommit = version.Version, version.Commit
	case "kubectl-client-version-json":
		var version struct {
			Client struct {
				Version  string `json:"gitVersion"`
				Platform string `json:"platform"`
			} `json:"clientVersion"`
		}
		if err := decodeSmokeVersion(raw, &version); err != nil {
			return member, err
		}
		if version.Client.Platform != member.Platform {
			return member, e.New(e.CodeVerifyFailed, "kubectl reports a different platform")
		}
		member.ObservedVersion = version.Client.Version
	}
	if member.ObservedVersion != member.ExpectedVersion || member.ObservedCommit != member.ExpectedCommit {
		return member, e.New(e.CodeVerifyFailed, "delivered tool version or commit differs from frozen producer")
	}
	return member, nil
}
func decodeSmokeVersion(raw []byte, value any) error {
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "ambiguous tool version JSON")
	}
	if err := json.Unmarshal(raw, value); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "invalid tool version JSON")
	}
	return nil
}
