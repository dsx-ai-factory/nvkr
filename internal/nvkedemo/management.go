// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

const managementBundle = "installer-tools-arm64"
const kindConfigPath = "controller/hack/controller-capi-kind.yaml"
const kindConfigHash = "835a53f6c852ddd71d2f26614131985383a5aa4deb7acf62e68435c328fa71f0"
const toolVersionsHash = "ae8980d7b31d9f2f9c355dd7d873854eaba8cfe94f5075ec61dfa9bdbdb5a0f9"
const kindNodeImage = "kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5"
const kindNodeID = "sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5"
const socketMount = "  extraMounts:\n    - hostPath: /var/run/docker.sock\n      containerPath: /var/run/docker.sock\n"
const sourceKindIPFamily = "  ipFamily: dual\n"
const managementPodCIDR = "10.244.0.0/16"
const managementServiceCIDR = "10.96.0.0/16"

var managementMembers = []string{"clusterctl", "helm", "kind", "kubectl", "oras"}

// ErrRetainedControls forbids removing controls still required by joined work.
var ErrRetainedControls = errors.New("dependent controls must be retained")

// NativeEnvironment excludes ambient targets and credentials. It is not a
// hermetic execution environment or an authenticated execution receipt.
func NativeEnvironment() []string {
	var env []string
	for _, key := range []string{"PATH", "USER", "LANG", "HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return SetEnv(env, map[string]string{"DOCKER_HOST": "unix:///var/run/docker.sock", "KIND_EXPERIMENTAL_PROVIDER": "docker", "CLUSTERCTL_DISABLE_VERSIONCHECK": "true"})
}

// SetEnv produces a sorted independent environment without duplicate keys.
func SetEnv(base []string, values map[string]string) []string {
	result := make(map[string]string)
	for _, item := range base {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			result[key] = value
		}
	}
	for key, value := range values {
		result[key] = value
	}
	keys := make([]string, 0, len(result))
	for key := range result {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+result[key])
	}
	return env
}

func environmentValue(env []string, key string) string {
	for _, item := range env {
		k, v, ok := strings.Cut(item, "=")
		if ok && k == key {
			return v
		}
	}
	return ""
}

func resolveExisting(path string) (string, error) {
	if path == "" {
		return "", errors.New("nonempty existing path required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(absolute)
}

func insidePath(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(os.PathSeparator)) && !filepath.IsAbs(relative)
}

func freshDestination(path string) (string, error) {
	if path == "" {
		return "", errors.New("nonempty new destination required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if _, err = os.Lstat(absolute); !errors.Is(err, fs.ErrNotExist) {
		return "", errors.New("choose a fresh private output directory")
	}
	parent, err := resolveExisting(filepath.Dir(absolute))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(absolute)), nil
}

func boundedRegularRead(path string, limit int64) ([]byte, error) {
	held, info, err := securepath.OpenRegular(path)
	if err != nil {
		return nil, err
	}
	defer held.Close()
	if info.Size() > limit {
		return nil, errors.New("regular input exceeds diagnostic bound")
	}
	raw, err := io.ReadAll(io.LimitReader(held, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("regular input exceeds diagnostic bound")
	}
	return raw, nil
}

func privateWrite(path string, raw []byte) error {
	held, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, writeErr := held.Write(raw)
	return errors.Join(writeErr, held.Close())
}

func privateJSON(path string, value any) error {
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return privateWrite(path, append(raw, '\n'))
}

func privateReplace(path string, raw []byte) error {
	if _, err := boundedRegularRead(path, diagnosticLimit); err != nil {
		return err
	}
	held, err := os.CreateTemp(filepath.Dir(path), ".nvkr-projection-")
	if err != nil {
		return err
	}
	name := held.Name()
	defer func() { _ = os.Remove(name) }()
	if err = held.Chmod(0o600); err == nil {
		_, err = held.Write(raw)
	}
	err = errors.Join(err, held.Close())
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func regularTree(root string) error {
	held, _, err := securepath.OpenDirectory(root)
	if err != nil {
		return err
	}
	_ = held.Close()
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("require regular producer layout without symlinks")
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return errors.New("nonregular producer member")
		}
		return nil
	})
}

func diagnosticIndexMatches(layout, digest string) error {
	parent, err := os.Lstat(filepath.Dir(layout))
	if err != nil {
		return err
	}
	if !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 {
		return errors.New("producer parent is not a regular directory")
	}
	if err := regularTree(layout); err != nil {
		return err
	}
	raw, err := boundedRegularRead(filepath.Join(layout, "index.json"), diagnosticLimit)
	if err != nil {
		return err
	}
	var index v1.IndexManifest
	if err = json.Unmarshal(raw, &index); err != nil {
		return err
	}
	if len(index.Manifests) != 1 || index.Manifests[0].Digest.String() != digest {
		return errors.New("selected layout differs from completed Run manifest")
	}
	return nil
}

func captureJSON[T any](ctx context.Context, runner Runner, args []string, phase string) (T, error) {
	var value T
	raw, err := runner.Capture(ctx, args, phase, 30*time.Second)
	if err != nil {
		return value, err
	}
	err = json.Unmarshal(raw, &value)
	return value, err
}

func sourceBytes(ctx context.Context, runner Runner, checkout, path, phase string) ([]byte, error) {
	return runner.Capture(ctx, []string{"git", "-C", checkout, "show", InstallerCommit + ":" + path}, phase, 30*time.Second)
}

func plainSHA(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

type managementAdmission struct {
	checkout, tool, layout, out string
	config                      []byte
	source                      map[string][]byte
	toolsOutput                 SelectedOutput
}

// projectManagementConfig changes only this demo's private runtime copy after
// exact source-byte admission. The projection selects IPv4 and, for either
// OVN mode, disables the fixture's default CNI and kube-proxy. The explicit
// kind-calico diagnostic keeps those source-owned defaults.
func projectManagementConfig(source []byte) ([]byte, error) {
	return projectManagementConfigForNetwork(source, "ovn")
}

func projectManagementConfigForNetwork(source []byte, selected string) ([]byte, error) {
	mode, err := normalizeNetwork(selected)
	if err != nil {
		return nil, err
	}
	text := string(source)
	if plainSHA(source) != kindConfigHash || strings.Count(text, socketMount) != 1 || strings.Count(text, sourceKindIPFamily) != 1 {
		return nil, errors.New("source-owned Kind configuration differs from inspected interface")
	}
	text = strings.Replace(text, socketMount, "", 1)
	network := "  ipFamily: ipv4\n"
	if mode != "kind-calico" {
		network += "  disableDefaultCNI: true\n  kubeProxyMode: none\n"
	}
	network += "  podSubnet: " + managementPodCIDR + "\n  serviceSubnet: " + managementServiceCIDR + "\n"
	return []byte(strings.Replace(text, sourceKindIPFamily, network, 1)), nil
}

func admitManagement(ctx context.Context, cfg Config) (managementAdmission, error) {
	var admitted managementAdmission
	mode, err := normalizeNetwork(cfg.Network)
	if err != nil {
		return admitted, err
	}
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		return admitted, errors.New("require native Linux ARM64 inside the dedicated VM")
	}
	if mode != "kind-calico" {
		if err := admitSiteOVNKernel(); err != nil {
			return admitted, err
		}
	}
	runner := Runner{Env: NativeEnvironment(), Progress: cfg.Progress}
	for _, name := range []string{"docker", "git", "python3"} {
		if _, err := commandPath(name, runner.Env); err != nil {
			return admitted, err
		}
	}
	checkout, err := resolveExisting(cfg.NVKECheckout)
	if err != nil {
		return admitted, err
	}
	tool, err := resolveExisting(cfg.NVKR)
	if err != nil {
		return admitted, err
	}
	info, err := os.Stat(tool)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return admitted, errors.New("require already built native nvkr")
	}
	root, err := resolveExisting(cfg.Runs.Tools)
	if err != nil {
		return admitted, err
	}
	out, err := freshDestination(cfg.Out)
	if err != nil {
		return admitted, err
	}
	if err = admitStackStateRoot(out); err != nil {
		return admitted, err
	}
	if insidePath(out, checkout) || insidePath(out, root) {
		return admitted, errors.New("diagnostic output must be outside source and accepted Run")
	}
	if err = verifyInstaller(ctx, runner, checkout); err != nil {
		return admitted, err
	}
	config, err := sourceBytes(ctx, runner, checkout, kindConfigPath, "admit-kind-config")
	if err != nil {
		return admitted, err
	}
	config, err = projectManagementConfigForNetwork(config, mode)
	if err != nil {
		return admitted, err
	}
	observation, err := CompleteOutput(ctx, root, managementBundle, "executable")
	if err != nil {
		return admitted, err
	}
	layout := filepath.Join(root, "packages", managementBundle)
	if err = diagnosticIndexMatches(layout, observation.Digest); err != nil {
		return admitted, err
	}
	type dockerInfo struct {
		OSType, Architecture string
		NCPU                 int
		MemTotal             uint64
	}
	facts, err := captureJSON[dockerInfo](ctx, runner, []string{"docker", "info", "--format", "{{json .}}"}, "admit-docker")
	if err != nil {
		return admitted, err
	}
	if facts.OSType != "linux" || !slices.Contains([]string{"aarch64", "arm64"}, facts.Architecture) || facts.NCPU < 8 || facts.MemTotal < 20<<30 {
		return admitted, errors.New("require native ARM64 Docker, 8 CPUs and 20 GiB RAM")
	}
	dir, _, err := securepath.OpenDirectory(filepath.Dir(out))
	if err != nil {
		return admitted, err
	}
	free, capacityErr := securepath.AvailableDiskBackedBytes(ctx, dir)
	_ = dir.Close()
	if capacityErr != nil {
		return admitted, capacityErr
	}
	if free < 20<<30 {
		return admitted, errors.New("management diagnostic requires 20 GiB native disk headroom")
	}
	if err := admitKindImage(ctx, runner); err != nil {
		return admitted, err
	}
	source := make(map[string][]byte)
	for i, path := range []string{"deploy/check-site.sh", "deploy/lib/common.sh", "make/tool-versions.mk"} {
		raw, readErr := sourceBytes(ctx, runner, checkout, path, fmt.Sprintf("admit-management-source-%d", i))
		if readErr != nil {
			return admitted, readErr
		}
		source[path] = raw
	}
	if plainSHA(source["make/tool-versions.mk"]) != toolVersionsHash {
		return admitted, errors.New("NVKE installer tool pins differ from inspected interface")
	}
	return managementAdmission{checkout: checkout, tool: tool, layout: layout, out: out, config: config, source: source, toolsOutput: SelectedOutput{Layout: layout, Observation: observation}}, nil
}

func admitKindImage(ctx context.Context, runner Runner) error {
	images, err := captureJSON[[]dockerImage](ctx, runner, []string{"docker", "image", "inspect", kindNodeImage}, "admit-kind-image")
	if err != nil {
		return fmt.Errorf("could not inspect the required cached ARM64 Kind image %s; verify Docker access and cache the pinned image as described in docs/local-nvke-demo.md#4-build-and-run-the-demo: %w", kindNodeImage, err)
	}
	if len(images) != 1 || images[0].ID != kindNodeID || images[0].Architecture != "arm64" {
		return fmt.Errorf("cache the exact source-pinned ARM64 Kind image %s before execution; see docs/local-nvke-demo.md#4-build-and-run-the-demo", kindNodeImage)
	}
	return nil
}

type dockerImage struct {
	ID           string `json:"Id"`
	Architecture string
}
type dockerMount struct {
	Type, Name, Destination, Driver string
	RW                              bool
}
type dockerContainer struct {
	ID          string `json:"Id"`
	Name, Image string
	Config      struct {
		Labels  map[string]string
		Volumes map[string]json.RawMessage
	}
	Mounts          []dockerMount
	State           struct{ Running bool }
	NetworkSettings struct {
		Networks map[string]struct{ IPAddress, NetworkID string }
	}
}
type dockerVolume struct {
	Name, Driver string
	Labels       map[string]string
}

func ownedManagementNodes(ctx context.Context, runner Runner, name string) ([]string, error) {
	raw, err := runner.Capture(ctx, []string{"docker", "ps", "-aq", "--no-trunc", "--filter", "label=io.x-k8s.kind.cluster=" + name}, "owned-management-inventory", 30*time.Second)
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(raw))
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := captureJSON[[]dockerContainer](ctx, runner, append([]string{"docker", "inspect"}, ids...), "owned-management-description")
	if err != nil {
		return nil, err
	}
	if err = validateOwnedNodes(rows, ids, name); err != nil {
		return nil, err
	}
	slices.Sort(ids)
	return ids, nil
}

func validateOwnedNodes(rows []dockerContainer, ids []string, name string) error {
	if len(rows) != len(ids) {
		return errors.New("incomplete management-node ownership description")
	}
	seen := make(map[string]bool)
	for _, row := range rows {
		if !slices.Contains(ids, row.ID) || seen[row.ID] || row.Config.Labels["io.x-k8s.kind.cluster"] != name || row.Config.Labels["io.x-k8s.kind.role"] != "control-plane" || row.Image != kindNodeID || row.Name != "/"+name+"-control-plane" {
			return errors.New("management-node ownership changed; refusing cleanup")
		}
		seen[row.ID] = true
	}
	return nil
}

func managementVolumes(ctx context.Context, runner Runner, ids []string, existing map[string]bool) ([]string, error) {
	rows, err := captureJSON[[]dockerContainer](ctx, runner, append([]string{"docker", "inspect"}, ids...), "owned-management-volumes")
	if err != nil {
		return nil, err
	}
	if len(rows) != len(ids) {
		return nil, errors.New("incomplete node-volume descriptions")
	}
	var volumes []string
	seenIDs := make(map[string]bool)
	for _, row := range rows {
		mounts := make([]dockerMount, 0)
		for _, mount := range row.Mounts {
			if mount.Type == "volume" {
				mounts = append(mounts, mount)
			}
		}
		if !slices.Contains(ids, row.ID) || seenIDs[row.ID] || len(mounts) != 1 {
			return nil, errors.New("unexpected management node-volume ownership")
		}
		seenIDs[row.ID] = true
		mount := mounts[0]
		_, varDeclared := row.Config.Volumes["/var"]
		if mount.Destination != "/var" || mount.Driver != "local" || !mount.RW || !varDeclared || mount.Name == "" || existing[mount.Name] || slices.Contains(volumes, mount.Name) {
			return nil, errors.New("management volume predates attempt,is shared or differs")
		}
		descriptions, readErr := captureJSON[[]dockerVolume](ctx, runner, []string{"docker", "volume", "inspect", mount.Name}, "owned-anonymous-volume")
		if readErr != nil {
			return nil, readErr
		}
		if len(descriptions) != 1 || descriptions[0].Name != mount.Name || descriptions[0].Driver != "local" {
			return nil, errors.New("not a Docker-issued anonymous local volume")
		}
		if _, ok := descriptions[0].Labels["com.docker.volume.anonymous"]; !ok {
			return nil, errors.New("refusing named volume cleanup")
		}
		consumers, readErr := runner.Capture(ctx, []string{"docker", "ps", "-aq", "--no-trunc", "--filter", "volume=" + mount.Name}, "owned-volume-consumers", 30*time.Second)
		if readErr != nil {
			return nil, readErr
		}
		if !slices.Equal(strings.Fields(string(consumers)), []string{row.ID}) {
			return nil, errors.New("management volume has foreign consumers")
		}
		volumes = append(volumes, mount.Name)
	}
	slices.Sort(volumes)
	return volumes, nil
}

func cleanupManagement(ctx context.Context, session ManagementSession, existing map[string]bool) error {
	// Cleanup inventories and removal never require writable diagnostics.
	runner := session.Runner
	runner.Work = ""
	ids, err := ownedManagementNodes(ctx, runner, session.Name)
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		volumes, readErr := managementVolumes(ctx, runner, ids, existing)
		if readErr != nil {
			return readErr
		}
		_ = privateJSON(filepath.Join(session.Work, "node-ownership.json"), struct {
			Cluster          string
			NodeIDs, Volumes []string
		}{session.Name, ids, volumes})
		if err = runner.Run(ctx, append([]string{"docker", "rm", "--force", "--volumes"}, ids...), "owned-node-cleanup", 60*time.Second); err != nil {
			return err
		}
		raw, readErr := runner.Capture(ctx, []string{"docker", "volume", "ls", "-q"}, "post-cleanup-volumes", 30*time.Second)
		if readErr != nil {
			return readErr
		}
		for _, volume := range strings.Fields(string(raw)) {
			if slices.Contains(volumes, volume) {
				return errors.New("owned anonymous volume remains after cleanup")
			}
		}
	}
	remaining, err := ownedManagementNodes(ctx, runner, session.Name)
	if err != nil {
		return err
	}
	if len(remaining) > 0 {
		return errors.New("owned management nodes remain after cleanup")
	}
	return nil
}

// ManagementSession is a live diagnostic lifetime, not an NVKR receipt.
type ManagementSession struct {
	Name, Tools, Work string
	Runner            Runner
	tenantProfiles    *tenantProfilesBinding
}

func cleanupAfterJoinedWork(ctx context.Context, primary error, timeout time.Duration, cleanup func(context.Context) error) error {
	if holdErr := waitRetainedCleanup(ctx, primary); holdErr != nil {
		return errors.Join(primary, holdErr)
	}
	if errors.Is(primary, ErrUnjoined) || errors.Is(primary, ErrRetainedControls) {
		recordRetainedCleanup(ctx, primary)
		return primary
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	cleanupErr := cleanup(cleanupCtx)
	recordRetainedCleanup(ctx, cleanupErr)
	return errors.Join(primary, cleanupErr)
}

func verifyToolMembers(tools string, members []string) error {
	rows, err := os.ReadDir(tools)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		info, statErr := os.Lstat(filepath.Join(tools, row.Name()))
		if statErr != nil {
			return statErr
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			return errors.New("require regular executable tool members")
		}
		names = append(names, row.Name())
	}
	if !slices.Equal(names, members) {
		return errors.New("unexpected executable tool membership")
	}
	return nil
}

func verifyToolVersions(ctx context.Context, session ManagementSession) error {
	for _, pin := range []struct {
		name, expected string
		args           []string
	}{{"kind", "0.32.0", []string{"version", "-q"}}, {"clusterctl", "v1.13.6", []string{"version", "-o", "short"}}, {"helm", "v4.2.4", []string{"version", "--template", "{{.Version}}"}}} {
		raw, err := session.Runner.Capture(ctx, append([]string{filepath.Join(session.Tools, pin.name)}, pin.args...), "version-"+pin.name, 30*time.Second)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(raw)) != pin.expected {
			return errors.New("built tool does not match pinned installer version: " + pin.name)
		}
	}
	type kubectlVersion struct {
		ClientVersion struct{ GitVersion, Platform string }
	}
	version, err := captureJSON[kubectlVersion](ctx, session.Runner, []string{filepath.Join(session.Tools, "kubectl"), "version", "--client", "-o", "json"}, "version-kubectl")
	if err != nil {
		return err
	}
	if version.ClientVersion.GitVersion != "v1.36.0" || version.ClientVersion.Platform != "linux/arm64" {
		return errors.New("built kubectl version/platform differs from installer")
	}
	raw, err := session.Runner.Capture(ctx, []string{filepath.Join(session.Tools, "oras"), "version"}, "version-oras", 30*time.Second)
	if err != nil {
		return err
	}
	versionCount, archCount := 0, 0
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if slices.Equal(fields, []string{"Version:", "1.3.3"}) {
			versionCount++
		}
		if slices.Equal(fields, []string{"OS/Arch:", "linux/arm64"}) {
			archCount++
		}
	}
	if versionCount != 1 || archCount != 1 {
		return errors.New("built ORAS version/platform differs from installer")
	}
	return nil
}

// WithManagement creates one exact owned node and keeps it alive through the
// callback. Failed stop/join or retained controls prohibit dependent cleanup.
func WithManagement(ctx context.Context, cfg Config, consumer func(context.Context, ManagementSession) error) (result error) {
	if consumer == nil {
		return errors.New("management requires the site OVN consumer before readiness")
	}
	admitted, err := admitManagement(ctx, cfg)
	if err != nil {
		return err
	}
	var random [8]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	name := "nvkr-demo-" + hex.EncodeToString(random[:])
	preflight := Runner{Env: NativeEnvironment(), Progress: cfg.Progress}
	ids, err := ownedManagementNodes(ctx, preflight, name)
	if err != nil {
		return err
	}
	if len(ids) > 0 {
		return errors.New("fresh management identity already exists")
	}
	if err = os.Mkdir(admitted.out, 0o700); err != nil {
		return err
	}
	home := filepath.Join(admitted.out, "home")
	if err = os.Mkdir(home, 0o700); err != nil {
		return err
	}
	runner := Runner{Work: admitted.out, Env: SetEnv(preflight.Env, map[string]string{"HOME": home}), Progress: cfg.Progress}
	publication := filepath.Join(admitted.out, "publication")
	tools := filepath.Join(admitted.out, "tools")
	if err = runner.Run(ctx, []string{admitted.tool, "publish", "root", "--bundle", managementBundle + "=" + admitted.layout, "--out", publication}, "verify-tools", 120*time.Second); err != nil {
		return err
	}
	if err = runner.Run(ctx, []string{admitted.tool, "publish", "extract", "--layout", publication, "--bundle", managementBundle, "--out", tools}, "extract-tools", 120*time.Second); err != nil {
		return err
	}
	if err = verifyToolMembers(tools, managementMembers); err != nil {
		return err
	}
	runner.Env = SetEnv(runner.Env, map[string]string{"PATH": tools + string(os.PathListSeparator) + environmentValue(runner.Env, "PATH")})
	session := ManagementSession{Name: name, Tools: tools, Work: admitted.out, Runner: runner}
	if err = verifyToolVersions(ctx, session); err != nil {
		return err
	}
	session.Runner.Env = SetEnv(runner.Env, map[string]string{"SITE_KUBECONFIG": filepath.Join(admitted.out, "kubeconfig"), "PHASE_TIMEOUT": "10m", "REQUIRE_TIMEOUT": "600s"})
	if err = privateWrite(filepath.Join(admitted.out, "kind.yaml"), admitted.config); err != nil {
		return err
	}
	raw, err := preflight.Capture(ctx, []string{"docker", "volume", "ls", "-q"}, "initial-volume-inventory", 30*time.Second)
	if err != nil {
		return err
	}
	existing := make(map[string]bool)
	for _, volume := range strings.Fields(string(raw)) {
		existing[volume] = true
	}
	toolDigests := make(map[string]string)
	for _, member := range managementMembers {
		digest, hashErr := DigestFile(ctx, filepath.Join(tools, member))
		if hashErr != nil {
			return hashErr
		}
		toolDigests[member] = digest
	}
	sourceDigests := make(map[string]string)
	for path, content := range admitted.source {
		sourceDigests[path] = "sha256:" + plainSHA(content)
	}
	volumes := make([]string, 0, len(existing))
	for volume := range existing {
		volumes = append(volumes, volume)
	}
	slices.Sort(volumes)
	if err = privateJSON(filepath.Join(admitted.out, "inputs.json"), struct {
		Scope           string             `json:"scope"`
		Cluster         string             `json:"cluster"`
		InstallerCommit string             `json:"installerCommit"`
		Tools           lifecycle.Artifact `json:"tools"`
		ToolMembers     map[string]string  `json:"toolMembers"`
		NodeImage       string             `json:"nodeImage"`
		ConfigSHA256    string             `json:"configSHA256"`
		InitialVolumes  []string           `json:"initialVolumes"`
		SourceSHA256    map[string]string  `json:"sourceSHA256"`
	}{"unqualified-local-management-diagnostic", name, InstallerCommit, admitted.toolsOutput.Observation, toolDigests, kindNodeImage, "sha256:" + plainSHA(admitted.config), volumes, sourceDigests}); err != nil {
		return err
	}
	for path, content := range admitted.source {
		target := filepath.Join(admitted.out, "source", path)
		if err = os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err = privateWrite(target, content); err != nil {
			return err
		}
		if err = os.Chmod(target, 0o400); err != nil {
			return err
		}
	}
	defer func() {
		result = cleanupAfterJoinedWork(ctx, result, retainedManagementCleanupTimeout, func(cleanupCtx context.Context) error { return cleanupManagement(cleanupCtx, session, existing) })
	}()
	defer recoverRetainedPanic(ctx, &result)
	if err = session.Runner.Run(ctx, []string{filepath.Join(tools, "kind"), "create", "cluster", "--name", name, "--config", filepath.Join(admitted.out, "kind.yaml"), "--kubeconfig", environmentValue(session.Runner.Env, "SITE_KUBECONFIG")}, "create-management", 600*time.Second); err != nil {
		return err
	}
	ids, err = ownedManagementNodes(ctx, preflight, name)
	if err != nil {
		return err
	}
	if len(ids) != 1 {
		return errors.New("expected exactly one owned management node")
	}
	return consumer(ctx, session)
}
