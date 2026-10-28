// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/google/go-containerregistry/pkg/name"
	"go.yaml.in/yaml/v3"
)

const (
	tenantMetadataLimit = 256 << 10
	tenantIPXELimit     = 16384
	tenantUserDataLimit = 65536
)

type tenantProfileMetadata struct {
	Profiles map[string]struct {
		Kubernetes string `yaml:"kubernetes_version"`
		Injected   bool   `yaml:"kubernetes_version_injected"`
		Arch       string `yaml:"architecture"`
		BuildDate  string `yaml:"build_date"`
		Ubuntu     string `yaml:"ubuntu_version"`
		Variant    string `yaml:"variant"`
	} `yaml:"profiles"`
}

type tenantReleaseDocument struct {
	Metadata struct {
		Name        string            `json:"name"`
		Annotations map[string]string `json:"annotations"`
	} `json:"metadata"`
	Spec any `json:"spec"`
}

// Describe the already verified local disk and source-owned initialization.
// The metadata date comes from the diagnostic profile, not a build-time claim.
func tenantReleaseDocuments(network string, image lifecycle.Artifact, metadata, ipxe, userData []byte) (string, []tenantReleaseDocument, error) {
	invalid := func(message string) (string, []tenantReleaseDocument, error) {
		return "", nil, e.New(e.CodePreflightFailed, "%s", message)
	}
	diskOutput, err := selectedDiskOutput(network)
	if err != nil {
		return invalid("tenant release requires a supported network selection")
	}
	if image.Name != diskOutput || image.Kind != "disk-image" || image.OS != "linux" || image.Arch != "arm64" || !canonical.IsDigest(image.Digest) || image.Disk == nil || image.Disk.Size <= 0 || image.Disk.Format != "qcow2" || image.Disk.Encryption != "none" || image.Disk.PayloadDigest != image.Digest {
		return invalid("tenant release requires the verified local plain ARM64 QCOW2 output")
	}
	for i, raw := range [][]byte{metadata, ipxe, userData} {
		limit := []int{tenantMetadataLimit, tenantIPXELimit, tenantUserDataLimit}[i]
		if len(raw) == 0 || len(raw) > limit || !utf8.Valid(raw) || bytes.ContainsRune(raw, '\x00') {
			return invalid("tenant profile data is empty, oversized or not UTF-8 text")
		}
	}
	var profiles tenantProfileMetadata
	decoder := yaml.NewDecoder(bytes.NewReader(metadata))
	decoder.KnownFields(true)
	if err := decoder.Decode(&profiles); err != nil {
		return "", nil, e.Wrap(e.CodePreflightFailed, err, "decode source-owned tenant profile metadata")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return invalid("tenant profile metadata must contain one document")
	}
	profile, exists := profiles.Profiles[TenantProfile]
	if !exists || len(profiles.Profiles) != 1 || profile.Arch != image.Arch || profile.Ubuntu != "26.04" || profile.Variant != "local-plain-tpm" || !profile.Injected || profile.Kubernetes != "" {
		return invalid("source-owned tenant profile does not match the selected plain ARM64 image")
	}
	if _, err := time.Parse("2006-01-02", profile.BuildDate); err != nil {
		return invalid("source-owned tenant profile has no valid build date")
	}
	if !bytes.HasPrefix(ipxe, []byte("#!ipxe\n")) || !bytes.HasPrefix(userData, []byte("#cloud-config\n")) {
		return invalid("tenant profile is missing its iPXE or cloud-init header")
	}
	wantURL := "http://" + GuestGateway + ":18080/" + image.Name + ".qcow2"
	imageURLs := 0
	for _, line := range strings.Split(string(ipxe), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "set" && fields[1] == "image-url" {
			if len(fields) != 3 || fields[2] != wantURL {
				return invalid("tenant profile does not select the verified local disk endpoint")
			}
			imageURLs++
		}
	}
	if imageURLs != 1 {
		return invalid("tenant profile requires exactly one local disk endpoint")
	}
	name := "nvkr-local-" + strings.TrimPrefix(image.Digest, "sha256:")[:40]
	annotations := map[string]string{
		"nvkr.dev/diagnostic-disk-digest": image.Digest,
		"nvkr.dev/diagnostic-disk-size":   strconv.FormatInt(image.Disk.Size, 10),
	}
	var machine tenantReleaseDocument
	machine.Metadata.Name, machine.Metadata.Annotations = name, annotations
	machine.Spec = map[string]any{
		"operatingSystem": map[string]string{"family": "ubuntu", "version": profile.Ubuntu},
		"architecture":    profile.Arch, "variant": profile.Variant, "buildDate": profile.BuildDate,
		"nico": map[string]string{"ipxeScript": string(ipxe)},
	}
	var release tenantReleaseDocument
	release.Metadata.Name = name
	release.Metadata.Annotations = map[string]string{
		"nvkr.dev/diagnostic-disk-digest":                                   image.Digest,
		"internal.nvke.nvidia.com/machine-init-kubernetes-version":          "",
		"internal.nvke.nvidia.com/machine-init-kubernetes-version-injected": "true",
	}
	release.Spec = map[string]any{
		"machineImageRef": map[string]string{"name": name},
		"machineInit":     map[string]string{"userDataTemplate": string(userData)},
		"lifecycle":       map[string]bool{"deprecated": false, "hidden": false},
	}
	return name, []tenantReleaseDocument{machine, release, release}, nil
}

type nightlyReleaseResource struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	tenantReleaseDocument
}

func startNightlyDiskService(ctx context.Context, path, address string, profile NightlyProfile) (*diskService, error) {
	selected, err := selectedDiskOutput(profile.Network)
	if err != nil || (profile.Network != "ovn" && profile.Network != "upstream-ovn") || selected != profile.DiskOutput || filepath.Base(path) != DiskOutputName+".qcow2" {
		return nil, e.New(e.CodeEngineCapability, "nightly disk service requires the exact profile output and candidate filename")
	}
	alias := ""
	if selected != DiskOutputName {
		alias = selected + ".qcow2"
	}
	return startDiskServiceWithNightlyAlias(ctx, path, address, alias)
}

// The publication owner already authenticated and extracted these files. The
// shared diagnostic projection validates the selected disk and profile bytes;
// this adapter adds only the current Kubernetes envelope for the suite's fresh
// management cluster. No converter executable is built from another revision.
func nightlyProfileResources(ctx context.Context, profile NightlyProfile, release lifecycle.Release, directory, destination string) error {
	if err := requireContext(ctx); err != nil {
		return e.Wrap(e.CodeTimeout, err, "nightly release resources context")
	}
	if profile.OSProfile != TenantProfile {
		return e.New(e.CodeEngineCapability, "nightly suite requires its explicit local plain OS profile")
	}
	var image lifecycle.Artifact
	count := 0
	for _, artifact := range release.Artifacts {
		if artifact.Name == profile.DiskOutput {
			image, count = artifact, count+1
		}
	}
	if count != 1 {
		return e.New(e.CodeVerifyFailed, "nightly profile requires one exact selected disk output")
	}
	var files [][]byte
	for i, path := range []string{"metadata.yaml", profile.OSProfile + "/ipxeScript", profile.OSProfile + "/userData.tmpl"} {
		raw, err := boundedRegularRead(filepath.Join(directory, "profiles/osProfiles", path), []int64{tenantMetadataLimit, tenantIPXELimit, tenantUserDataLimit}[i])
		if err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "reading authenticated nightly profile %s", path)
		}
		files = append(files, raw)
	}
	_, documents, err := tenantReleaseDocuments(profile.Network, image, files[0], files[1], files[2])
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "projecting authenticated nightly profile")
	}
	// The upstream candidate suite admits this fixed profile name. Its fresh
	// cluster cannot contain an older resource with that name. Keep the full
	// verified disk digest annotations and exact boot/template bytes unchanged.
	for i := range documents {
		documents[i].Metadata.Name = profile.OSProfile
		if i != 0 {
			documents[i].Spec.(map[string]any)["machineImageRef"] = map[string]string{"name": profile.OSProfile}
		}
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "nightly release resources canceled")
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "creating nightly release resources")
	}
	for i, kind := range []string{"MachineImage", "ClusterRelease", "NodePoolRelease"} {
		resource := nightlyReleaseResource{APIVersion: "internal.nvke.nvidia.com/v1", Kind: kind, tenantReleaseDocument: documents[i]}
		// JSON is YAML; the suite's metadata reader admits .yaml documents.
		if err := privateJSON(filepath.Join(destination, profile.OSProfile+"-"+strings.ToLower(kind)+".yaml"), resource); err != nil {
			return e.Wrap(e.CodeImmutableViolation, err, "writing nightly %s", kind)
		}
	}
	return nil
}

// Only stack rendering consumes this spec. The suite's pinned Kind config owns
// management Kubernetes and networking; the loader-required bootstrap fields
// never trigger a bootstrap command or select an additional product artifact.
func nightlySiteSpec(ctx context.Context, runner Runner, source, gateway, controller, profiles string) (string, error) {
	if err := requireContext(ctx); err != nil {
		return "", e.Wrap(e.CodeTimeout, err, "nightly site spec context")
	}
	cluster := environmentValue(runner.Env, "KIND_CLUSTER_NAME")
	address, err := netip.ParseAddr(gateway)
	if err != nil || !address.Is4() || !nightlyOutputNamePattern.MatchString(cluster) || strings.ContainsAny(cluster, "._") || len(cluster+"-control-plane") > 63 {
		return "", e.New(e.CodeVerifyFailed, "nightly site requires its exact IPv4 simulator gateway and Kind name")
	}
	for _, reference := range []string{controller, profiles} {
		digest, err := name.NewDigest(reference, name.StrictValidation)
		if err != nil || digest.Name() != reference {
			return "", e.New(e.CodeVerifyFailed, "nightly site requires exact native controller and profiles references")
		}
	}
	telemetry, err := boundedRegularRead(filepath.Join(source, "nktp/hack/e2e-values.yaml"), diagnosticLimit)
	if err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "reading pinned nightly Kind telemetry values")
	}
	if err := privateWrite(filepath.Join(runner.Work, "nightly-nktp-values.yaml"), telemetry); err != nil {
		return "", e.Wrap(e.CodeImmutableViolation, err, "writing private nightly telemetry input")
	}
	spec := map[string]any{
		"clusterName": cluster,
		"cluster": map[string]any{
			"nodes":      []any{map[string]string{"name": cluster + "-control-plane", "ip": GuestGateway}},
			"k8sVersion": "1.37.0", "cniName": "ovn-kubernetes", "controlPlaneVIP": GuestGateway,
			"podCidr": managementPodCIDR, "serviceCidr": managementServiceCIDR,
		},
		"spire": map[string]any{"trustDomain": cluster + ".nke.local", "upstreamCA": true},
	}
	stack := map[string]any{
		"nico":   map[string]string{"endpoint": "http://" + gateway + ":18089", "orgID": "novxwn552ssc", "apiName": "forge"},
		"images": map[string]string{"controller": controller, "profiles": profiles},
	}
	if err := privateJSON(filepath.Join(runner.Work, "stack-config.json"), stack); err != nil {
		return "", e.Wrap(e.CodeImmutableViolation, err, "writing private nightly stack config")
	}
	path := filepath.Join(runner.Work, "sitespec.json")
	if err := privateJSON(path, spec); err != nil {
		return "", e.Wrap(e.CodeImmutableViolation, err, "writing private nightly site spec")
	}
	return path, nil
}

// nightlyStackEnvironment binds credentials to the child environment, never the public input files.
func nightlyStackEnvironment(runner Runner, site string) (Runner, error) {
	password, err := randomScalar()
	if err != nil {
		return runner, e.Wrap(e.CodePreflightFailed, err, "creating nightly node password")
	}
	database, err := randomScalar()
	if err != nil {
		return runner, e.Wrap(e.CodePreflightFailed, err, "creating nightly database password")
	}
	runner = bindNativeSpireCA(runner, runner.Work)
	// This adapter intentionally replaces inherited credentials with the fixed
	// local simulator protocol token and fresh diagnostic node/database values.
	// The token matches the pinned harness; it is not a real site credential.
	runner.Env = SetEnv(runner.Env, map[string]string{
		"NKE_E2E_CANDIDATE_SITESPEC":     site,
		"NKE_E2E_CANDIDATE_STACK_CONFIG": filepath.Join(runner.Work, "stack-config.json"),
		"NKE_E2E_CANDIDATE_NKTP_VALUES":  filepath.Join(runner.Work, "nightly-nktp-values.yaml"),
		"NVKECTL_NICO_TOKEN":             "nico-vmsctl-token", "NVKECTL_NODE_USER": "nvkr-nightly",
		"NVKECTL_NODE_PASSWORD": password, "NVKECTL_DATABASE_PASSWORD": database,
	})
	return runner, nil
}
