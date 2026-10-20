// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"go.yaml.in/yaml/v3"
)

const (
	tenantMetadataLimit = 256 << 10
	tenantIPXELimit     = 16384
	tenantUserDataLimit = 65536
)

// A live diagnostic join issued after site image observation. It cannot be
// restored from site-inputs.json and grants no NVKR publication authority.
type tenantProfilesBinding struct {
	podUID, profiles, controller string
}

func bindTenantProfiles(refs map[string]string, deployed map[string]any) (*tenantProfilesBinding, error) {
	profiles, ok := deployed["profiles"].(map[string]string)
	if !ok || profiles["podUID"] == "" || profiles["imageID"] != refs["profiles"] || profiles["path"] != "/profiles" || refs["cwe"] == "" || refs["profiles"] == "" {
		return nil, nvkrerr.New(nvkrerr.CodePreflightFailed, "require the live observed profiles image before tenant release creation")
	}
	return &tenantProfilesBinding{podUID: profiles["podUID"], profiles: refs["profiles"], controller: refs["cwe"]}, nil
}

func (binding *tenantProfilesBinding) validate(pod sitePod) error {
	if binding == nil || binding.podUID == "" || pod.Metadata.UID != binding.podUID || pod.Metadata.Name == "" || pod.Metadata.Namespace != installerControllerName {
		return nvkrerr.New(nvkrerr.CodePreflightFailed, "profiles pod identity changed before tenant release creation")
	}
	if err := validateProfilesImage(pod, installerControllerName, binding.profiles); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "tenant release profiles image")
	}
	containers, states := 0, 0
	for _, container := range pod.Spec.Containers {
		if container.Name == installerControllerName {
			containers++
			if container.Image != binding.controller {
				return nvkrerr.New(nvkrerr.CodePreflightFailed, "tenant release controller image changed")
			}
		}
	}
	for _, state := range pod.Status.ContainerStatuses {
		if state.Name == installerControllerName {
			states++
			if state.ImageID != binding.controller || !state.Ready || state.State.Running == nil {
				return nvkrerr.New(nvkrerr.CodePreflightFailed, "tenant release controller is not ready at its observed image")
			}
		}
	}
	if containers != 1 || states != 1 {
		return nvkrerr.New(nvkrerr.CodePreflightFailed, "tenant release requires exactly one observed controller container")
	}
	return nil
}

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
		return "", nil, nvkrerr.New(nvkrerr.CodePreflightFailed, "%s", message)
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
		return "", nil, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "decode source-owned tenant profile metadata")
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

func (t *tenant) tenantProfilePod(ctx context.Context) (sitePod, error) {
	var pods podList
	raw, err := t.capture(ctx, t.kubectl(environmentValue(t.session.Runner.Env, "SITE_KUBECONFIG"), "--request-timeout=10s", "-n", installerControllerName, "get", "pods", "-l", "app.kubernetes.io/name="+installerControllerName, "-o", "json"), "release-profiles-pod", 30*time.Second)
	if err == nil {
		err = json.Unmarshal(raw, &pods)
	}
	if err != nil {
		return sitePod{}, nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "observe tenant profiles pod")
	}
	if len(pods.Items) != 1 {
		return sitePod{}, nvkrerr.New(nvkrerr.CodePreflightFailed, "require one tenant profiles pod")
	}
	return pods.Items[0], t.session.tenantProfiles.validate(pods.Items[0])
}

func (t *tenant) prepareTenantReleases(ctx context.Context) (string, error) {
	if err := requireContext(ctx); err != nil {
		return "", err
	}
	if t.session.tenantProfiles == nil {
		return "", nvkrerr.New(nvkrerr.CodePreflightFailed, "tenant release creation requires a live profiles image observation")
	}
	pod, err := t.tenantProfilePod(ctx)
	if err != nil {
		return "", err
	}
	var files [][]byte
	for i, path := range []string{"metadata.yaml", TenantProfile + "/ipxeScript", TenantProfile + "/userData.tmpl"} {
		limit := []int{tenantMetadataLimit, tenantIPXELimit, tenantUserDataLimit}[i]
		// Only these fixed source-owned paths are read; an oversized file yields
		// one extra byte and fails before creating any resource or request file.
		script := `set -eu; test ! -L /profiles; test ! -L /profiles/osProfiles; test ! -L "$1"; test ! -L "${1%/*}"; test -f "$1"; head -c "$2" "$1"`
		raw, readErr := t.capture(ctx, t.kubectl(environmentValue(t.session.Runner.Env, "SITE_KUBECONFIG"), "--request-timeout=10s", "-n", installerControllerName, "exec", pod.Metadata.Name, "-c", installerControllerName, "--", "sh", "-ec", script, "nvkr-profile-read", "/profiles/osProfiles/"+path, strconv.Itoa(limit+1)), "release-profile-read", 30*time.Second)
		if readErr != nil {
			return "", nvkrerr.Wrap(nvkrerr.CodePreflightFailed, readErr, "read source-owned tenant profile")
		}
		files = append(files, raw)
	}
	if _, err = t.tenantProfilePod(ctx); err != nil {
		return "", err
	}
	name, documents, err := tenantReleaseDocuments(t.network, t.image, files[0], files[1], files[2])
	if err != nil {
		return "", err
	}
	for i, kind := range []string{"machineimage", "clusterrelease", "nodepoolrelease"} {
		path := filepath.Join(t.session.Work, "tenant-"+kind+".json")
		if err = privateJSON(path, documents[i]); err != nil {
			return "", nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "write tenant %s request", kind)
		}
		if _, err = t.capture(ctx, []string{t.cli(), "create", kind, "-f", path}, "create-"+kind, time.Minute); err != nil {
			return "", nvkrerr.Wrap(nvkrerr.CodePreflightFailed, err, "create tenant %s", kind)
		}
	}
	return name, nil
}
