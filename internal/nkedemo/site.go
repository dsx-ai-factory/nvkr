// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nkedemo

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/lifecycle"
	"go.yaml.in/yaml/v3"
)

const registryImage = "docker.io/library/registry@sha256:ddf754342cfc8acc51a56d5d0ab6af06826461864460636d8bd5c546dab2a7b8"
const registryBytes int64 = 1 << 30

var chartOutputs = map[string]string{"controller": "controller-chart", "nktp": "nktp", "postgresql": "postgresql", "spire": "spire", "spire-crds": "spire-crds", "temporal": "temporal"}
var runtimeImages = []string{"csi-node-driver-registrar", "postgresql", "spiffe-csi", "spire-agent", "spire-bash", "spire-controller-manager", "spire-oidc", "spire-server", "spire-tpm-server", "temporal-admin-tools", "temporal-server", "temporal-ui"}
var temporalTags = map[string]string{"temporal-server": "1.30.3", "temporal-admin-tools": "1.30.3", "temporal-ui": "2.48.1"}

// SelectedOutput is an observed immutable attempt description. nkdctl and ORAS,
// not this struct, verify byte integrity at extraction and registry handoff.
type SelectedOutput struct {
	Layout      string
	Observation lifecycle.Artifact
}
type SiteMaterials struct {
	Outputs                                      map[string]SelectedOutput
	Gateway, RegistryID, KindCIDR, KindNetworkID string
}
type SiteConnection struct{ Token, Endpoint, Org string }

// The source-owned chart copies the selected content image into its emptyDir.
func profilesImageValues(reference string) map[string]any {
	repository, digest, _ := strings.Cut(reference, "@")
	return map[string]any{
		"profiles": map[string]any{"path": "/profiles", "image": map[string]any{
			"enabled": true, "repository": repository, "digest": digest, "tag": ""}},
	}
}

func imageKeys() []string {
	keys := []string{"cwe", "profiles", "provider"}
	for _, name := range runtimeImages {
		keys = append(keys, "runtime-"+name)
	}
	return keys
}

func selectSiteOutput(ctx context.Context, root, name, kind string) (SelectedOutput, error) {
	var selected SelectedOutput
	root, err := resolveExisting(root)
	if err != nil {
		return selected, err
	}
	observation, err := CompleteOutput(ctx, root, name, kind)
	if err != nil {
		return selected, err
	}
	layout := filepath.Join(root, "packages", name)
	if err = diagnosticIndexMatches(layout, observation.Digest); err != nil {
		return selected, err
	}
	return SelectedOutput{Layout: layout, Observation: observation}, nil
}

func registryCapacity(outputs map[string]SelectedOutput) (int64, error) {
	blobs := make(map[string]int64)
	for _, key := range imageKeys() {
		selected, ok := outputs[key]
		if !ok {
			return 0, errors.New("missing registry input: " + key)
		}
		root := filepath.Join(selected.Layout, "blobs/sha256")
		rows, err := os.ReadDir(root)
		if err != nil {
			return 0, err
		}
		for _, row := range rows {
			info, readErr := os.Lstat(filepath.Join(root, row.Name()))
			if readErr != nil {
				return 0, readErr
			}
			if !info.Mode().IsRegular() || info.Size() > registryBytes {
				return 0, errors.New("require bounded regular registry blobs")
			}
			if size, seen := blobs[row.Name()]; seen && size != info.Size() {
				return 0, errors.New("conflicting registry blob sizes")
			}
			blobs[row.Name()] = info.Size()
			if len(blobs) > 4096 {
				return 0, errors.New("registry blob count exceeds bound")
			}
		}
	}
	var sum, largest int64
	for _, size := range blobs {
		sum += size
		if size > largest {
			largest = size
		}
	}
	required := sum + largest + (64 << 20)
	if required > registryBytes {
		return 0, fmt.Errorf("local registry capacity exceeded before creation:%d/%d bytes", required, registryBytes)
	}
	return required, nil
}

// AdmitSite validates the entire host, source, tools and site selection before
// creating outputs or granting simulator/registry/network controls.
func AdmitSite(ctx context.Context, cfg Config) (*SiteMaterials, error) {
	admitted, err := admitManagement(ctx, cfg)
	if err != nil {
		return nil, err
	}
	runner := Runner{Env: NativeEnvironment(), Progress: cfg.Progress}
	for _, name := range []string{"openssl", "bash", "curl"} {
		if _, err = commandPath(name, runner.Env); err != nil {
			return nil, err
		}
	}
	outputs := make(map[string]SelectedOutput)
	names := make([]string, 0, len(chartOutputs))
	for item := range chartOutputs {
		names = append(names, item)
	}
	slices.Sort(names)
	for _, item := range names {
		root := cfg.Runs.Components
		if item == "controller" || item == "nktp" {
			root = cfg.Runs.Site
		}
		selected, readErr := selectSiteOutput(ctx, root, chartOutputs[item], "helm-oci")
		if readErr != nil {
			return nil, readErr
		}
		outputs[item] = selected
	}
	for _, item := range []struct{ key, root, name, kind string }{{"cwe", cfg.Runs.Site, "cwe", "oci"}, {"profiles", cfg.Runs.Profiles, "controller-profiles", "oci"}, {"provider", cfg.Runs.Providers, "capnico-controller-arm64", "oci"}, {"nkectl", cfg.Runs.Site, "nkectl", "executable"}} {
		selected, readErr := selectSiteOutput(ctx, item.root, item.name, item.kind)
		if readErr != nil {
			return nil, readErr
		}
		outputs[item.key] = selected
	}
	for _, name := range runtimeImages {
		selected, readErr := selectSiteOutput(ctx, cfg.Runs.Components, name+"-image-arm64", "oci")
		if readErr != nil {
			return nil, readErr
		}
		outputs["runtime-"+name] = selected
	}
	if _, err = registryCapacity(outputs); err != nil {
		return nil, err
	}
	for _, selected := range outputs {
		if insidePath(filepath.Dir(admitted.out), filepath.Dir(filepath.Dir(selected.Layout))) {
			return nil, errors.New("site diagnostic must be outside accepted Runs")
		}
	}
	images, err := captureJSON[[]dockerImage](ctx, runner, []string{"docker", "image", "inspect", registryImage}, "admit-registry-image")
	if err != nil {
		return nil, err
	}
	if len(images) != 1 || images[0].Architecture != "arm64" || images[0].ID == "" {
		return nil, errors.New("cache exact ARM64 registry service before site execution")
	}
	type networkDescription struct {
		ID, Driver string
		IPAM       struct {
			Config []struct{ Gateway, Subnet string }
		}
	}
	networks, err := captureJSON[[]networkDescription](ctx, runner, []string{"docker", "network", "inspect", "kind"}, "admit-kind-network")
	if err != nil {
		return nil, err
	}
	if len(networks) != 1 || networks[0].Driver != "bridge" || networks[0].ID == "" {
		return nil, errors.New("require existing Kind bridge network")
	}
	gateway, cidr := "", ""
	for _, config := range networks[0].IPAM.Config {
		address, readErr := netip.ParseAddr(config.Gateway)
		if readErr != nil {
			return nil, errors.New("invalid Kind gateway")
		}
		if !address.Is4() {
			continue
		}
		if gateway != "" || !address.IsPrivate() {
			return nil, errors.New("ambiguous private Kind IPv4 gateway")
		}
		prefix, readErr := netip.ParsePrefix(config.Subnet)
		if readErr != nil || !prefix.Contains(address) {
			return nil, errors.New("invalid Kind subnet")
		}
		gateway, cidr = address.String(), prefix.String()
	}
	if gateway == "" {
		return nil, errors.New("missing private Kind IPv4 gateway")
	}
	return &SiteMaterials{Outputs: outputs, Gateway: gateway, RegistryID: images[0].ID, KindCIDR: cidr, KindNetworkID: networks[0].ID}, nil
}

func validateEndpoint(value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", errors.New("invalid NICoSim endpoint")
	}
	address, err := netip.ParseAddr(parsed.Hostname())
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" || !address.Is4() || !address.IsPrivate() || address.IsLoopback() || address.IsUnspecified() || parsed.Port() != "18089" {
		return "", errors.New("require private non-loopback NICoSim HTTP endpoint on port18089")
	}
	return strings.TrimRight(value, "/"), nil
}

func localJSONRequest(ctx context.Context, address string, timeout time.Duration, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return err
	}
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := http.Client{Timeout: timeout, Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("local service did not return200")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if err != nil {
		return err
	}
	if len(raw) > 4096 {
		return errors.New("local response exceeds bound")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		return err
	}
	if err = decoder.Decode(new(json.RawMessage)); err != io.EOF {
		return errors.New("trailing local service response")
	}
	return nil
}

func registryCleanup(ctx context.Context, runner Runner, name, owner, imageID string) error {
	runner.Work = ""
	raw, err := runner.Capture(ctx, []string{"docker", "ps", "-aq", "--no-trunc", "--filter", "name=^/" + name + "$"}, "registry-cleanup-inventory", 30*time.Second)
	if err != nil {
		return err
	}
	ids := strings.Fields(string(raw))
	if len(ids) == 0 {
		return nil
	}
	rows, err := captureJSON[[]dockerContainer](ctx, runner, append([]string{"docker", "inspect"}, ids...), "registry-cleanup-description")
	if err != nil {
		return err
	}
	if err = validateRegistryOwnership(rows, ids, name, owner, imageID); err != nil {
		return err
	}
	if err = runner.Run(ctx, []string{"docker", "rm", "--force", ids[0]}, "registry-cleanup", 60*time.Second); err != nil {
		return err
	}
	raw, err = runner.Capture(ctx, []string{"docker", "ps", "-aq", "--filter", "name=^/" + name + "$"}, "registry-disappearance", 30*time.Second)
	if err != nil {
		return err
	}
	if len(strings.Fields(string(raw))) > 0 {
		return errors.New("private registry remains")
	}
	return nil
}

func validateRegistryOwnership(rows []dockerContainer, ids []string, name, owner, imageID string) error {
	if len(ids) != 1 || len(rows) != 1 || rows[0].ID != ids[0] || rows[0].Name != "/"+name || rows[0].Image != imageID || rows[0].Config.Labels["dev.nkd.demo.owner"] != owner {
		return errors.New("private registry ownership changed;refusing removal")
	}
	for _, mount := range rows[0].Mounts {
		if mount.Type == "volume" || mount.Type == "bind" {
			return errors.New("private registry acquired persistent storage;refusing removal")
		}
	}
	return nil
}

func registryAddress(rows []dockerContainer, id, name, owner string, materials *SiteMaterials) (string, error) {
	if materials == nil || id == "" || materials.RegistryID == "" || materials.KindNetworkID == "" {
		return "", errors.New("registry omits its admitted container or network identity")
	}
	if err := validateRegistryOwnership(rows, []string{id}, name, owner, materials.RegistryID); err != nil {
		return "", err
	}
	prefix, err := netip.ParsePrefix(materials.KindCIDR)
	if err != nil || prefix != prefix.Masked() || prefix.String() != materials.KindCIDR || !prefix.Addr().Is4() || !prefix.Addr().IsPrivate() || !networkLast(prefix).IsPrivate() {
		return "", errors.New("registry requires the admitted private Kind subnet")
	}
	gateway, err := netip.ParseAddr(materials.Gateway)
	if err != nil || !gateway.Is4() || gateway.String() != materials.Gateway || !prefix.Contains(gateway) {
		return "", errors.New("registry requires the admitted Kind gateway")
	}
	row := rows[0]
	network, present := row.NetworkSettings.Networks["kind"]
	address, err := netip.ParseAddr(network.IPAddress)
	if !row.State.Running || len(row.NetworkSettings.Networks) != 1 || !present || network.NetworkID != materials.KindNetworkID ||
		err != nil || !address.Is4() || !address.IsPrivate() || address.String() != network.IPAddress ||
		!prefix.Contains(address) || address == gateway || address == prefix.Addr() || address == networkLast(prefix) {
		return "", errors.New("owned registry requires its exact running Kind-network IPv4 address")
	}
	return net.JoinHostPort(address.String(), "5000"), nil
}

func startSiteRegistry(ctx context.Context, runner Runner, name, owner, node string, materials *SiteMaterials) (string, error) {
	raw, err := runner.Capture(ctx, []string{"docker", "run", "-d", "--name", name, "--label", "dev.nkd.demo.owner=" + owner, "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--network", "kind", "--tmpfs", fmt.Sprintf("/var/lib/registry:rw,size=%d", registryBytes), registryImage}, "start-registry", 60*time.Second)
	if err != nil {
		return "", err
	}
	ids := strings.Fields(string(raw))
	if len(ids) != 1 {
		return "", errors.New("registry start did not return one container identity")
	}
	host, err := waitRegistry(ctx, runner, ids[0], name, owner, materials)
	if err != nil {
		return "", err
	}
	if err = probeNodeRegistry(ctx, runner, node, host); err != nil {
		return "", err
	}
	return host, nil
}

func probeNodeRegistry(ctx context.Context, runner Runner, node, host string) error {
	// Use the node's real proxy environment, as pinned NKE readiness and staging
	// do. A forced bypass here would admit a route those later stages cannot use.
	raw, err := runner.Capture(ctx, []string{"docker", "exec", node, "curl", "--fail", "--silent", "--show-error", "--max-time", "10", "http://" + host + "/v2/"}, "node-registry-connectivity", 15*time.Second)
	if err != nil {
		return fmt.Errorf("owned Kind node cannot reach the admitted registry; private node-registry-connectivity diagnostics retained: %w", err)
	}
	if !bytes.Equal(bytes.TrimSpace(raw), []byte("{}")) {
		return errors.New("owned Kind node registry probe did not return the exact empty v2 response")
	}
	return nil
}

func waitRegistry(ctx context.Context, runner Runner, id, name, owner string, materials *SiteMaterials) (string, error) {
	budget, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	probe := runner
	probe.Work = ""
	for budget.Err() == nil {
		rows, err := captureJSON[[]dockerContainer](budget, probe, []string{"docker", "inspect", id}, "registry-startup-state")
		if err != nil {
			return "", err
		}
		host, err := registryAddress(rows, id, name, owner, materials)
		if err != nil {
			return "", err
		}
		var response map[string]json.RawMessage
		if err = localJSONRequest(budget, "http://"+host+"/v2/", 2*time.Second, &response); err == nil && response != nil && len(response) == 0 {
			return host, nil
		}
		timer := time.NewTimer(200 * time.Millisecond)
		select {
		case <-budget.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	diagnosticCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer stop()
	if err := runner.Run(diagnosticCtx, []string{"docker", "logs", "--tail", "100", name}, "registry-startup-diagnostics", 30*time.Second); errors.Is(err, ErrUnjoined) {
		return "", err
	}
	return "", fmt.Errorf("registry failed startup budget;private diagnostics retained: %w", budget.Err())
}

type pinnedImage struct {
	Registry   string `json:"registry,omitempty"`
	Repository string `json:"repository"`
	Tag        string `json:"tag,omitempty"`
	Digest     string `json:"digest,omitempty"`
}
type imageOverride struct {
	Image pinnedImage `json:"image"`
}
type spireServerOverride struct {
	Image             pinnedImage   `json:"image"`
	ControllerManager imageOverride `json:"controllerManager"`
	NodeAttestor      struct {
		TPMDirect imageOverride `json:"tpmDirect"`
	} `json:"nodeAttestor"`
	Tests struct {
		Bash imageOverride `json:"bash"`
	} `json:"tests"`
}
type spireAgentOverride struct {
	Image           pinnedImage   `json:"image"`
	SocketAlternate imageOverride `json:"socketAlternate"`
	FSGroupFix      imageOverride `json:"fsGroupFix"`
}
type spireCSIOverride struct {
	Image               pinnedImage   `json:"image"`
	NodeDriverRegistrar imageOverride `json:"nodeDriverRegistrar"`
}
type spireOIDCOverride struct {
	Image pinnedImage `json:"image"`
	Tests struct {
		Bash imageOverride `json:"bash"`
	} `json:"tests"`
}
type spireOverrides struct {
	Server spireServerOverride `json:"spire-server"`
	Agent  spireAgentOverride  `json:"spire-agent"`
	CSI    spireCSIOverride    `json:"spiffe-csi-driver"`
	OIDC   spireOIDCOverride   `json:"spiffe-oidc-discovery-provider"`
}
type postgresOverrides struct {
	Global struct {
		Security struct {
			AllowInsecureImages bool `json:"allowInsecureImages"`
		} `json:"security"`
	} `json:"global"`
	Image pinnedImage `json:"image"`
}
type temporalOverrides struct {
	Server     imageOverride `json:"server"`
	AdminTools imageOverride `json:"admintools"`
	Web        imageOverride `json:"web"`
}

func runtimeValues(host string, outputs map[string]SelectedOutput) map[string]any {
	image := func(key string) pinnedImage {
		return pinnedImage{Registry: host, Repository: "nkd-local/" + key, Tag: outputs["runtime-"+key].Observation.Digest}
	}
	var spire spireOverrides
	spire.Server.Image = image("spire-server")
	spire.Server.ControllerManager.Image = image("spire-controller-manager")
	spire.Server.NodeAttestor.TPMDirect.Image = image("spire-tpm-server")
	spire.Server.Tests.Bash.Image = image("spire-bash")
	spire.Agent.Image = image("spire-agent")
	spire.Agent.SocketAlternate.Image = image("spire-bash")
	spire.Agent.FSGroupFix.Image = image("spire-bash")
	spire.CSI.Image = image("spiffe-csi")
	spire.CSI.NodeDriverRegistrar.Image = image("csi-node-driver-registrar")
	spire.OIDC.Image = image("spire-oidc")
	spire.OIDC.Tests.Bash.Image = image("spire-bash")
	var postgres postgresOverrides
	postgres.Global.Security.AllowInsecureImages = true
	postgres.Image = pinnedImage{Registry: host, Repository: "nkd-local/postgresql", Digest: outputs["runtime-postgresql"].Observation.Digest}
	temporalImage := func(key string) imageOverride {
		return imageOverride{Image: pinnedImage{Repository: host + "/nkd-local/" + key, Tag: temporalTags[key] + "@" + outputs["runtime-"+key].Observation.Digest}}
	}
	return map[string]any{"spire": spire, "postgresql": postgres, "temporal": temporalOverrides{Server: temporalImage("temporal-server"), AdminTools: temporalImage("temporal-admin-tools"), Web: temporalImage("temporal-ui")}}
}

func appendRuntimeValues(raw []byte) ([]byte, error) {
	return appendValuesFile(raw, "nkd-image-inputs.json")
}

func appendValuesFile(raw []byte, filename string) ([]byte, error) {
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	start := -1
	for i, line := range lines {
		if line == "values:" {
			if start != -1 {
				return nil, errors.New("duplicate values header")
			}
			start = i
		}
	}
	if start < 0 {
		return nil, errors.New("missing inspected values form")
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		line := lines[i]
		if line != "" && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "#") {
			end = i
			break
		}
	}
	result := slices.Clone(lines[:end])
	result = append(result, "  - "+filename)
	result = append(result, lines[end:]...)
	return []byte(strings.Join(result, "\n") + "\n"), nil
}

func appendChartValues(ctx context.Context, capture chartCapture, reader, deploy, item, filename string, values any) error {
	release := filepath.Join(deploy, item, "release.yaml")
	before, err := releaseFields(ctx, capture, reader, release)
	if err != nil {
		return err
	}
	if before[7] == "" {
		return errors.New("chart overrides require existing values")
	}
	raw, err := boundedRegularRead(release, diagnosticLimit)
	if err != nil {
		return err
	}
	projected, err := appendValuesFile(raw, filename)
	if err != nil {
		return err
	}
	if err = privateJSON(filepath.Join(filepath.Dir(release), filename), values); err != nil {
		return err
	}
	if err = privateReplace(release, projected); err != nil {
		return err
	}
	expected := slices.Clone(before)
	expected[7] += "," + filename
	after, err := releaseFields(ctx, capture, reader, release)
	if err != nil {
		return err
	}
	if !slices.Equal(after, expected) {
		return errors.New("chart override changed other deploy semantics")
	}
	return nil
}

func prepareRuntimeValues(ctx context.Context, checkout, deploy, host string, outputs map[string]SelectedOutput, capture chartCapture) error {
	values := runtimeValues(host, outputs)
	reader := filepath.Join(checkout, "deploy/lib/nke-deploy-items.py")
	for _, item := range []string{"postgresql", "spire", "temporal"} {
		if err := appendChartValues(ctx, capture, reader, deploy, item, "nkd-image-inputs.json", values[item]); err != nil {
			return err
		}
	}
	return nil
}

func copySiteImages(ctx context.Context, session ManagementSession, host string, outputs map[string]SelectedOutput) (map[string]string, error) {
	refs := make(map[string]string)
	address, _, err := net.SplitHostPort(host)
	if err != nil {
		return nil, err
	}
	runner := session.Runner
	for _, key := range []string{"NO_PROXY", "no_proxy"} {
		value := environmentValue(runner.Env, key)
		if value != "" {
			value += ","
		}
		runner.Env = SetEnv(runner.Env, map[string]string{key: value + address})
	}
	for _, key := range imageKeys() {
		selected := outputs[key]
		target := host + "/nkd-local/" + strings.TrimPrefix(key, "runtime-") + ":" + session.Name
		if err := runner.Run(ctx, []string{filepath.Join(session.Tools, "oras"), "cp", "--concurrency", "1", "--from-oci-layout", selected.Layout + "@" + selected.Observation.Digest, target, "--to-plain-http"}, "copy-"+key, 180*time.Second); err != nil {
			return nil, err
		}
		var descriptor struct{ Digest string }
		raw, err := runner.Capture(ctx, []string{filepath.Join(session.Tools, "oras"), "manifest", "fetch", "--plain-http", "--descriptor", target}, "readback-"+key, 30*time.Second)
		if err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &descriptor); err != nil {
			return nil, err
		}
		if descriptor.Digest != selected.Observation.Digest {
			return nil, errors.New("registry changed observed native manifest: " + key)
		}
		refs[key] = host + "/nkd-local/" + strings.TrimPrefix(key, "runtime-") + "@" + selected.Observation.Digest
	}
	return refs, nil
}

type podMetadata struct {
	UID, Namespace string
	Labels         map[string]string
}
type profileMount struct {
	Name, MountPath string
	ReadOnly        bool
}
type profileVolume struct {
	Name     string
	HostPath *struct{ Path, Type string }
	EmptyDir *json.RawMessage
}
type podContainer struct {
	Name, Image  string
	VolumeMounts []profileMount
}
type podContainerState struct {
	Name, ImageID string
	Ready         bool
	State         struct {
		Running    *json.RawMessage
		Terminated *struct{ ExitCode *int }
	}
}
type sitePod struct {
	Metadata podMetadata
	Spec     struct {
		Containers, InitContainers []podContainer
		Volumes                    []profileVolume
	}
	Status struct {
		ContainerStatuses, InitContainerStatuses []podContainerState
		Phase                                    string
	}
}
type podList struct{ Items []sitePod }
type podRole struct{ Kind, Name, Component string }
type runtimeRoleBinding struct {
	Namespace, Instance, Application string
	Roles                            []podRole
}

func runtimeBindings() map[string]runtimeRoleBinding {
	return map[string]runtimeRoleBinding{
		"csi-node-driver-registrar": {"spire-system", "spire", "spiffe-csi-driver", []podRole{{"containers", "node-driver-registrar", ""}}},
		"postgresql":                {"postgres", "postgres", "postgresql", []podRole{{"containers", "postgresql", "primary"}}},
		"spiffe-csi":                {"spire-system", "spire", "spiffe-csi-driver", []podRole{{"containers", "spiffe-csi-driver", ""}}},
		"spire-agent":               {"spire-system", "spire", "agent", []podRole{{"containers", "spire-agent", ""}}},
		"spire-bash":                {"spire-system", "spire", "agent", []podRole{{"initContainers", "ensure-alternate-names", ""}, {"initContainers", "fsgroupfix", ""}}},
		"spire-controller-manager":  {"spire-server", "spire", "server", []podRole{{"containers", "spire-controller-manager", ""}}},
		"spire-oidc":                {"spire-server", "spire", "spiffe-oidc-discovery-provider", []podRole{{"containers", "spiffe-oidc-discovery-provider", ""}}},
		"spire-server":              {"spire-server", "spire", "server", []podRole{{"containers", "spire-server", ""}}},
		"spire-tpm-server":          {"spire-server", "spire", "server", []podRole{{"initContainers", "init-tpm-direct", ""}}},
		"temporal-admin-tools":      {"temporal", "temporal", "temporal", []podRole{{"containers", "admin-tools", "admintools"}}},
		"temporal-server":           {"temporal", "temporal", "temporal", []podRole{{"containers", "temporal-frontend", "frontend"}, {"containers", "temporal-history", "history"}, {"containers", "temporal-matching", "matching"}, {"containers", "temporal-worker", "worker"}}},
		"temporal-ui":               {"temporal", "temporal", "temporal", []podRole{{"containers", "temporal-web", "web"}}},
	}
}

type observedPodContainer struct {
	PodUID    string `json:"podUID"`
	Namespace string `json:"namespace"`
	Container string `json:"container"`
	Kind      string `json:"kind"`
	ImageID   string `json:"imageID"`
}
type observedRuntime struct {
	Status     string                 `json:"status"`
	Containers []observedPodContainer `json:"containers"`
}

func validateRuntimePods(rows podList, refs map[string]string) (map[string]observedRuntime, error) {
	result := make(map[string]observedRuntime)
	bindings := runtimeBindings()
	for _, name := range runtimeImages {
		key := "runtime-" + name
		reference := refs[key]
		repository, digest, ok := strings.Cut(reference, "@")
		if !ok || repository == "" || digest == "" {
			return nil, errors.New("missing accepted runtime reference")
		}
		spellings := []string{reference}
		if tag, exists := temporalTags[name]; exists {
			spellings = append(spellings, repository+":"+tag+"@"+digest)
		}
		binding := bindings[name]
		roles := slices.Clone(binding.Roles)
		if name == "temporal-admin-tools" {
			roles = append(roles, podRole{"containers", "done", "database"})
			for _, operation := range []string{"create", "setup", "update"} {
				for _, store := range []string{"default", "visibility"} {
					roles = append(roles, podRole{"initContainers", operation + "-" + store + "-store", "database"})
				}
			}
		}
		seen := make(map[podRole]bool)
		var members []observedPodContainer
		for _, pod := range rows.Items {
			labels := pod.Metadata.Labels
			selected := pod.Metadata.Namespace == binding.Namespace && labels["app.kubernetes.io/instance"] == binding.Instance && labels["app.kubernetes.io/name"] == binding.Application
			for _, field := range []struct {
				kind       string
				containers []podContainer
				states     []podContainerState
			}{{"containers", pod.Spec.Containers, pod.Status.ContainerStatuses}, {"initContainers", pod.Spec.InitContainers, pod.Status.InitContainerStatuses}} {
				for _, container := range field.containers {
					var matched []podRole
					for _, role := range roles {
						if selected && role.Kind == field.kind && role.Name == container.Name && (role.Component == "" || role.Component == labels["app.kubernetes.io/component"]) {
							matched = append(matched, role)
						}
					}
					if len(matched) == 0 && !strings.HasPrefix(container.Image, repository+"@") && !strings.HasPrefix(container.Image, repository+":") {
						continue
					}
					var states []podContainerState
					for _, state := range field.states {
						if state.Name == container.Name {
							states = append(states, state)
						}
					}
					if !slices.Contains(spellings, container.Image) || len(states) != 1 || states[0].ImageID != reference {
						return nil, errors.New("deployed dependency differs from accepted native manifest: " + key)
					}
					state := states[0]
					completed := state.State.Terminated != nil && state.State.Terminated.ExitCode != nil && *state.State.Terminated.ExitCode == 0
					ready := completed
					if field.kind == "containers" {
						ready = (state.Ready && state.State.Running != nil) || (completed && pod.Status.Phase == "Succeeded")
					}
					if !ready {
						return nil, errors.New("deployed dependency not ready or completed: " + key)
					}
					if pod.Metadata.UID == "" {
						return nil, errors.New("missing deployed pod UID")
					}
					for _, role := range matched {
						seen[role] = true
					}
					members = append(members, observedPodContainer{PodUID: pod.Metadata.UID, Namespace: pod.Metadata.Namespace, Container: container.Name, Kind: field.kind, ImageID: state.ImageID})
				}
			}
		}
		for _, role := range binding.Roles {
			if !seen[role] {
				return nil, errors.New("missing enabled pod role for accepted dependency: " + key)
			}
		}
		slices.SortFunc(members, func(a, b observedPodContainer) int {
			return strings.Compare(a.Namespace+"\x00"+a.PodUID+"\x00"+a.Kind+"\x00"+a.Container, b.Namespace+"\x00"+b.PodUID+"\x00"+b.Kind+"\x00"+b.Container)
		})
		result[key] = observedRuntime{Status: "observed", Containers: members}
	}
	return result, nil
}

func deployedSiteImages(ctx context.Context, session ManagementSession, refs map[string]string) (map[string]any, error) {
	observed := make(map[string]any)
	kubectl := filepath.Join(session.Tools, "kubectl")
	kube := environmentValue(session.Runner.Env, "SITE_KUBECONFIG")
	for _, item := range []struct {
		key, namespace, selector, container string
	}{{"cwe", "nke-controller", "app.kubernetes.io/name=nke-controller", "nke-controller"}, {"provider", "capnico-system", "control-plane=controller-manager", "manager"}} {
		rows, err := captureJSON[podList](ctx, session.Runner, []string{kubectl, "--kubeconfig", kube, "-n", item.namespace, "get", "pods", "-l", item.selector, "-o", "json"}, "deployed-"+item.key)
		if err != nil {
			return nil, err
		}
		if len(rows.Items) != 1 {
			return nil, errors.New("expected one exact deployed image pod")
		}
		if item.key == "cwe" {
			if err := validateProfilesImage(rows.Items[0], item.container, refs["profiles"]); err != nil {
				return nil, err
			}
			observed["profiles"] = map[string]string{"podUID": rows.Items[0].Metadata.UID, "imageID": refs["profiles"], "path": "/profiles"}
		}
		states := rows.Items[0].Status.ContainerStatuses
		var selected []podContainerState
		for _, state := range states {
			if state.Name == item.container {
				selected = append(selected, state)
			}
		}
		if len(selected) != 1 || selected[0].ImageID != refs[item.key] || !selected[0].Ready || rows.Items[0].Metadata.UID == "" {
			return nil, errors.New("deployed image does not match accepted manifest/readiness: " + item.key)
		}
		observed[item.key] = struct {
			PodUID  string `json:"podUID"`
			ImageID string `json:"imageID"`
		}{rows.Items[0].Metadata.UID, selected[0].ImageID}
	}
	rows, err := captureJSON[podList](ctx, session.Runner, []string{kubectl, "--kubeconfig", kube, "get", "pods", "-A", "-o", "json"}, "deployed-runtime-images")
	if err != nil {
		return nil, err
	}
	runtime, err := validateRuntimePods(rows, refs)
	if err != nil {
		return nil, err
	}
	for key, value := range runtime {
		observed[key] = value
	}
	return observed, nil
}

func validateProfilesImage(pod sitePod, controller, reference string) error {
	inits, states, volumes, mounts := 0, 0, 0, 0
	overlaps := func(mountPath, root string) bool {
		mountPath = filepath.Clean(mountPath)
		return mountPath == "/" || mountPath == root || strings.HasPrefix(mountPath, root+"/")
	}
	for _, init := range pod.Spec.InitContainers {
		if init.Name != "materialize-profiles" {
			continue
		}
		inits++
		if init.Image != reference {
			return errors.New("profiles init container differs from the selected image or copy destination")
		}
		destinations := 0
		for _, mount := range init.VolumeMounts {
			if mount.Name == "profiles" || overlaps(mount.MountPath, "/profiles-out") {
				destinations++
				if mount.Name != "profiles" || mount.MountPath != "/profiles-out" || mount.ReadOnly {
					return errors.New("profiles init container differs from the selected copy destination")
				}
			} else if overlaps(mount.MountPath, "/profiles") {
				return errors.New("profiles init source is masked by another volume")
			}
		}
		if destinations != 1 {
			return errors.New("profiles init requires exactly one writable copy destination")
		}
	}
	for _, state := range pod.Status.InitContainerStatuses {
		if state.Name != "materialize-profiles" {
			continue
		}
		states++
		if state.ImageID != reference || state.State.Terminated == nil ||
			state.State.Terminated.ExitCode == nil || *state.State.Terminated.ExitCode != 0 {
			return errors.New("selected profiles image has not completed materialization successfully")
		}
	}
	for _, volume := range pod.Spec.Volumes {
		if volume.Name == "profiles" {
			volumes++
			if volume.EmptyDir == nil || volume.HostPath != nil {
				return errors.New("profiles must use the chart-owned emptyDir, not a host directory")
			}
		}
	}
	for _, container := range pod.Spec.Containers {
		if container.Name != controller {
			continue
		}
		for _, mount := range container.VolumeMounts {
			if mount.Name == "profiles" || overlaps(mount.MountPath, "/profiles") {
				mounts++
				if mount.Name != "profiles" || mount.MountPath != "/profiles" || !mount.ReadOnly {
					return errors.New("deployed profiles must be mounted read-only at the exact controller path")
				}
			}
		}
	}
	if inits != 1 || states != 1 || volumes != 1 || mounts != 1 {
		return errors.New("controller requires exactly one completed profiles init container and shared mount")
	}
	return nil
}

func bootstrapCA(ctx context.Context, session ManagementSession) error {
	cert, key := filepath.Join(session.Work, "ca.crt"), filepath.Join(session.Work, "ca.key")
	if err := session.Runner.Run(ctx, []string{"openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1", "-subj", "/CN=nkd-local-demo", "-addext", "basicConstraints=critical,CA:TRUE", "-addext", "keyUsage=critical,keyCertSign,cRLSign", "-keyout", key, "-out", cert}, "local-bootstrap-ca", 30*time.Second); err != nil {
		return err
	}
	if err := os.Chmod(key, 0o600); err != nil {
		return err
	}
	return session.Runner.Run(ctx, []string{"openssl", "verify", "-x509_strict", "-CAfile", cert, cert}, "check-local-bootstrap-ca", 30*time.Second)
}

func randomScalar() (string, error) {
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

type siteConfiguration struct {
	Site struct {
		Name        string `yaml:"name"`
		TrustDomain string `yaml:"trustDomain"`
		VIP         string `yaml:"vip"`
		Arch        string `yaml:"arch"`
	} `yaml:"site"`
	Spire struct {
		CACertFile string `yaml:"caCertFile"`
		CAKeyFile  string `yaml:"caKeyFile"`
	} `yaml:"spire"`
	Images struct {
		Controller string `yaml:"controller"`
		Profiles   string `yaml:"profiles"`
	} `yaml:"images"`
	Nico struct {
		Endpoint string `yaml:"endpoint"`
		OrgID    string `yaml:"orgId"`
		Token    string `yaml:"token"`
		APIName  string `yaml:"apiName"`
	} `yaml:"nico"`
	Registry struct {
		Server   string `yaml:"server"`
		Username string `yaml:"username"`
		Token    string `yaml:"token"`
	} `yaml:"registry"`
	Tenant struct {
		User     string `yaml:"user"`
		Password string `yaml:"password"`
	} `yaml:"tenant"`
}

// checkoutSiteInstaller materializes the full pinned tree in this private copy.
// Sparse checkout writes extensions.worktreeConfig, which the shared source Git
// owner deliberately rejects. Keep that policy and the original source intact.
func checkoutSiteInstaller(ctx context.Context, runner Runner, source, checkout, commit string) error {
	if err := runner.Run(ctx, []string{"git", "clone", "--no-hardlinks", "--no-checkout", source, checkout}, "clone-installer", 120*time.Second); err != nil {
		return err
	}
	return runner.Run(ctx, []string{"git", "-C", checkout, "checkout", "--detach", commit}, "checkout-installer", 60*time.Second)
}

// ConsumeSite installs a site from exact selected outputs, retaining its private
// registry through the callback. No success is emitted before owned cleanup.
func ConsumeSite(ctx context.Context, cfg Config, materials *SiteMaterials, connection SiteConnection, session ManagementSession, consumer func(context.Context, ManagementSession) error) (result error) {
	if materials == nil {
		return errors.New("site materials admission required")
	}
	tool, err := resolveExisting(cfg.NKDCTL)
	if err != nil {
		return err
	}
	sourceCheckout, err := resolveExisting(cfg.NKECheckout)
	if err != nil {
		return err
	}
	endpoint, err := validateEndpoint(connection.Endpoint)
	if err != nil {
		return err
	}
	if connection.Token == "" || len(connection.Token) > 4096 || strings.ContainsAny(connection.Token, "\r\n") || connection.Org == "" {
		return errors.New("require bounded scalar NICo connection")
	}
	var health struct{ Status string }
	if err = localJSONRequest(ctx, endpoint+"/healthz", 3*time.Second, &health); err != nil {
		return err
	}
	if health.Status != "ok" {
		return errors.New("NICoSim health interface differs")
	}
	probe := session.Runner
	probe.Work = ""
	nodes, err := ownedManagementNodes(ctx, probe, session.Name)
	if err != nil {
		return err
	}
	if len(nodes) != 1 {
		return errors.New("site requires exactly owned management node")
	}
	node := nodes[0]
	rows, err := captureJSON[[]dockerContainer](ctx, probe, []string{"docker", "inspect", node}, "site-node-address")
	if err != nil {
		return err
	}
	if len(rows) != 1 {
		return errors.New("missing owned node description")
	}
	vip := rows[0].NetworkSettings.Networks["kind"].IPAddress
	address, err := netip.ParseAddr(vip)
	if err != nil || !address.Is4() {
		return errors.New("invalid site node address")
	}
	session.Runner.Env = SetEnv(session.Runner.Env, map[string]string{"STAGE_NEEDS_RELEASE": "1", "NODE_EXEC": "docker exec {node}", "PREPULL_EXTERNAL": "0"})
	selected := make(map[string]lifecycle.Artifact)
	for key, value := range materials.Outputs {
		selected[key] = value.Observation
	}
	if err = privateJSON(filepath.Join(session.Work, "site-selected-outputs.json"), struct {
		Scope   string                        `json:"scope"`
		Outputs map[string]lifecycle.Artifact `json:"outputs"`
	}{"unqualified-local-site-diagnostic", selected}); err != nil {
		return err
	}
	cliRoot, cliTools := filepath.Join(session.Work, "cli-publication"), filepath.Join(session.Work, "cli")
	if err = session.Runner.Run(ctx, []string{tool, "publish", "root", "--bundle", "nkectl=" + materials.Outputs["nkectl"].Layout, "--out", cliRoot}, "verify-site-cli", 120*time.Second); err != nil {
		return err
	}
	if err = session.Runner.Run(ctx, []string{tool, "publish", "extract", "--layout", cliRoot, "--bundle", "nkectl", "--out", cliTools}, "extract-site-cli", 120*time.Second); err != nil {
		return err
	}
	if err = verifyToolMembers(cliTools, []string{"nkectl"}); err != nil {
		return err
	}
	session.Runner.Env = SetEnv(session.Runner.Env, map[string]string{"PATH": cliTools + string(os.PathListSeparator) + environmentValue(session.Runner.Env, "PATH")})
	checkout := filepath.Join(session.Work, "installer")
	if err = checkoutSiteInstaller(ctx, session.Runner, sourceCheckout, checkout, InstallerCommit); err != nil {
		return err
	}
	charts, pins := make(map[string]string), make(map[string]string)
	for item := range chartOutputs {
		charts[item] = materials.Outputs[item].Layout
		pins[item] = materials.Outputs[item].Observation.Digest
	}
	capture := sequencedChartCapture(session.Runner)
	deploy, err := prepareChartInputs(ctx, ChartInputsConfig{NKECheckout: checkout, NKDCTL: tool, Out: filepath.Join(session.Work, "chart-inputs"), Charts: charts, Manifests: pins}, capture)
	if err != nil {
		return err
	}
	session.Runner.Env = SetEnv(session.Runner.Env, map[string]string{"DEPLOY_PATH": deploy})
	registryName := session.Name + "-registry"
	raw, err := probe.Capture(ctx, []string{"docker", "ps", "-aq", "--filter", "name=^/" + registryName + "$"}, "fresh-registry-identity", 30*time.Second)
	if err != nil {
		return err
	}
	if len(strings.Fields(string(raw))) > 0 {
		return errors.New("fresh private registry identity exists")
	}
	defer func() {
		result = cleanupAfterJoinedWork(ctx, result, 2*time.Minute, func(cleanupCtx context.Context) error {
			return registryCleanup(cleanupCtx, session.Runner, registryName, session.Name, materials.RegistryID)
		})
	}()
	host, err := startSiteRegistry(ctx, session.Runner, registryName, session.Name, node, materials)
	if err != nil {
		return err
	}
	if err = prepareRuntimeValues(ctx, checkout, deploy, host, materials.Outputs, capture); err != nil {
		return err
	}
	refs, err := copySiteImages(ctx, session, host, materials.Outputs)
	if err != nil {
		return err
	}
	hostConfig := filepath.Join(session.Work, "registry-hosts.toml")
	if err = privateWrite(hostConfig, []byte("server = \"http://"+host+"\"\n[host.\"http://"+host+"\"]\n  capabilities = [\"pull\", \"resolve\"]\n")); err != nil {
		return err
	}
	certs := "/etc/containerd/certs.d/" + host
	if err = session.Runner.Run(ctx, []string{"docker", "exec", node, "mkdir", "-p", certs}, "node-registry-dir", 30*time.Second); err != nil {
		return err
	}
	if err = session.Runner.Run(ctx, []string{"docker", "cp", hostConfig, node + ":" + certs + "/hosts.toml"}, "node-registry-config", 30*time.Second); err != nil {
		return err
	}
	if err = bootstrapCA(ctx, session); err != nil {
		return err
	}
	var config siteConfiguration
	config.Site.Name = session.Name
	config.Site.TrustDomain = "nkd.local"
	config.Site.VIP = vip
	config.Site.Arch = "arm64"
	config.Spire.CACertFile = filepath.Join(session.Work, "ca.crt")
	config.Spire.CAKeyFile = filepath.Join(session.Work, "ca.key")
	config.Images.Controller = refs["cwe"]
	// The source-owned chart consumes this exact native image through its init container.
	config.Images.Profiles = refs["profiles"]
	config.Nico.Endpoint = endpoint
	config.Nico.OrgID = connection.Org
	config.Nico.Token = connection.Token
	config.Nico.APIName = "forge"
	config.Registry.Server = host
	config.Registry.Username = "local-diagnostic"
	config.Registry.Token, err = randomScalar()
	if err != nil {
		return err
	}
	config.Tenant.User = "nkd-demo"
	config.Tenant.Password, err = randomScalar()
	if err != nil {
		return err
	}
	telemetry, err := sourceBytes(ctx, session.Runner, checkout, "nktp/hack/e2e-values.yaml", "kind-nktp-values")
	if err != nil {
		return err
	}
	if !strings.Contains(string(telemetry), "  args: [\"--kubelet-insecure-tls\"]") {
		return errors.New("selected NKE Kind telemetry fixture differs")
	}
	encoded, err := yaml.Marshal(config)
	if err != nil {
		return err
	}
	encoded = append(encoded, []byte("nktp:\n")...)
	for _, line := range strings.Split(strings.TrimSuffix(string(telemetry), "\n"), "\n") {
		encoded = append(encoded, []byte("  "+line+"\n")...)
	}
	siteConfig := filepath.Join(session.Work, "site-config.yaml")
	if err = privateWrite(siteConfig, encoded); err != nil {
		return err
	}
	manifest := filepath.Join(session.Work, "local-assembly.yaml")
	manifestBytes := "apiVersion: nke.nvidia.com/release/v1\nrelease: local-assembly\nlane: local-assembly\nscope: none\nsource:\n  repo: https://github.com/dsx-ai-factory/nke\n  tag: local-assembly\n  commit: " + InstallerCommit + "\nitems:\n  - {name: controller, type: image, ref: " + refs["cwe"] + "}\n"
	if err = privateWrite(manifest, []byte(manifestBytes)); err != nil {
		return err
	}
	session.Runner.Env = SetEnv(session.Runner.Env, map[string]string{"SITE_CONFIG": siteConfig, "RELEASE_FILE": manifest, "CAPNICO_IMAGE": strings.Split(refs["provider"], "@")[0] + ":" + session.Name, "API_CLIENT_CIDRS": materials.Gateway + "/32"})
	for _, phase := range []string{"00-check-readiness", "01-prepare-cluster", "02-stage-images", "03-describe-site", "05-install-site-controller", "06-secure-and-register"} {
		stage := filepath.Join(checkout, "deploy/stages", phase+".sh")
		if err = session.Runner.Run(ctx, []string{"bash", stage, "run"}, "site-"+phase, 1800*time.Second); err != nil {
			return err
		}
		if err = session.Runner.Run(ctx, []string{"bash", stage, "check"}, "check-"+phase, 180*time.Second); err != nil {
			return err
		}
		if phase == "03-describe-site" {
			if err = appendChartValues(ctx, capture, filepath.Join(checkout, "deploy/lib/nke-deploy-items.py"), deploy, "controller", "nkd-profiles-image.json", profilesImageValues(refs["profiles"])); err != nil {
				return err
			}
		}
	}
	images, err := deployedSiteImages(ctx, session, refs)
	if err != nil {
		return err
	}
	chartDigests := make(map[string]string)
	for item := range chartOutputs {
		digest, hashErr := DigestFile(ctx, filepath.Join(deploy, "charts", item+".tgz"))
		if hashErr != nil {
			return hashErr
		}
		chartDigests[item] = digest
	}
	if err = privateJSON(filepath.Join(session.Work, "site-inputs.json"), struct {
		Scope    string                        `json:"scope"`
		Refs     map[string]string             `json:"refs"`
		Deployed map[string]any                `json:"deployed"`
		Outputs  map[string]lifecycle.Artifact `json:"outputs"`
		Charts   map[string]string             `json:"charts"`
	}{"unqualified-local-site-diagnostic", refs, images, selected, chartDigests}); err != nil {
		return err
	}
	if consumer != nil {
		return consumer(ctx, session)
	}
	return nil
}
