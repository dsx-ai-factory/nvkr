// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"golang.org/x/sys/unix"
)

// Only the default Mac BUILD supplies these installed dependencies. Explicit
// customer-selected Definition/Environment inputs retain their existing meaning.
func defaultMacBuild(cfg *BuildConfig, platform, executable string) (bool, error) {
	if platform != "darwin/arm64" || cfg.Definition != "" || cfg.Environment != "" || cfg.NVKR != "" {
		return false, nil
	}
	if cfg.Prepared || cfg.QualifyLocal || cfg.QualificationPolicy != "" {
		return false, e.New(e.CodeEngineCapability, "default Mac BUILD supports unsigned local execution")
	}
	definition, environment, err := LocalArtifactInputsFor(cfg.Network, cfg.Conformance, "darwin")
	if err != nil {
		return false, err
	}
	cfg.Definition, cfg.Environment = definition, environment
	cfg.NVKR = filepath.Join(filepath.Dir(executable), "nvkr")
	return true, nil
}

// discoverMacPackerFiles selects the installed Homebrew tool roles used by the
// reviewed native driver. It issues no execution receipt: factory/delivery and
// the native execution owner capture and authenticate every selected byte.
func discoverMacPackerFiles(ctx context.Context, runner Runner, repository string) (delivery.BinaryBundleRequest, error) {
	request := delivery.BinaryBundleRequest{OS: "darwin", Arch: "arm64", Purpose: "packer-toolchain"}
	add := func(name, source, destination string, mode uint32) error {
		resolved, err := filepath.EvalSymlinks(source)
		if err != nil {
			return err
		}
		info, err := os.Lstat(resolved)
		if err != nil || !info.Mode().IsRegular() || mode&0o111 != 0 && info.Mode().Perm()&0o111 == 0 {
			return e.New(e.CodeEngineCapability, "native Packer prerequisite %q must be a regular file with its required mode", name)
		}
		request.Files = append(request.Files, delivery.BinaryFileInput{Name: name, Path: resolved, InstallPath: destination, Mode: mode})
		return nil
	}
	lookup := func(name string) (string, error) {
		path, err := commandPath(name, runner.Env)
		if err != nil {
			return "", e.Wrap(e.CodeEngineCapability, err, "native Mac BUILD prerequisite")
		}
		return filepath.EvalSymlinks(path)
	}
	// The catalog owns release versions and the exact plugin archive member.
	loaded, err := catalog.LoadContext(ctx, catalog.Dir(filepath.Join(repository, "inputs", "catalogs", "tools", "native")))
	if err != nil {
		return request, err
	}
	packerVersion, pluginName, err := macPackerPrerequisiteSelection(loaded.Components["packer"], loaded.Tools["packer-plugin-qemu"])
	if err != nil {
		return request, err
	}
	packer, err := lookup("packer")
	if err != nil {
		return request, err
	}
	version, err := runner.Capture(ctx, []string{packer, "version"}, "native-packer-prerequisite-version", 15*time.Second)
	if err != nil {
		return request, err
	}
	if strings.TrimSpace(string(version)) != "Packer v"+packerVersion {
		return request, e.New(e.CodeEngineCapability, "native Mac BUILD requires catalog-selected Packer %s", packerVersion)
	}
	plugins, err := runner.Capture(ctx, []string{packer, "plugins", "installed"}, "native-packer-prerequisite-plugin", 15*time.Second)
	if err != nil {
		return request, err
	}
	plugin := ""
	for line := range strings.SplitSeq(strings.TrimSpace(string(plugins)), "\n") {
		if filepath.Base(line) != pluginName {
			continue
		}
		if plugin != "" || !filepath.IsAbs(line) || filepath.Clean(line) != line {
			return request, e.New(e.CodeEngineCapability, "native Mac BUILD requires one exact installed QEMU plugin %s", pluginName)
		}
		plugin = line
	}
	if plugin == "" {
		return request, e.New(e.CodeEngineCapability, "native Mac BUILD requires catalog-selected QEMU plugin %s", pluginName)
	}
	for _, file := range []delivery.BinaryFileInput{
		{Name: "packer", Path: packer, InstallPath: "/usr/local/bin/packer", Mode: 0755},
		{Name: pluginName, Path: plugin, InstallPath: "/usr/local/lib/packer/plugins/github.com/hashicorp/qemu/" + pluginName, Mode: 0755},
		{Name: pluginName + "_SHA256SUM", Path: plugin + "_SHA256SUM", InstallPath: "/usr/local/lib/packer/plugins/github.com/hashicorp/qemu/" + pluginName + "_SHA256SUM", Mode: 0644},
	} {
		if err := add(file.Name, file.Path, file.InstallPath, file.Mode); err != nil {
			return request, err
		}
	}
	qemu, err := lookup("qemu-system-aarch64")
	if err != nil {
		return request, err
	}
	qemuImage, err := lookup("qemu-img")
	if err != nil {
		return request, err
	}
	if filepath.Dir(qemu) != filepath.Dir(qemuImage) {
		return request, e.New(e.CodeEngineCapability, "native Mac BUILD requires QEMU programs from the same installed prefix")
	}
	for _, name := range []string{"bash", "qemu-img", "qemu-system-aarch64", "python3"} {
		path, err := lookup(name)
		if err != nil {
			return request, err
		}
		if err := add(name, path, "/usr/local/bin/"+name, 0755); err != nil {
			return request, err
		}
	}
	for _, name := range []string{"dirname", "install", "mktemp", "rm", "chmod", "stat", "mv", "sha256sum", "cat", "mkdir", "cp", "tr", "head", "tail", "wc", "cut", "sort", "touch", "sleep", "readlink", "ln", "tee"} {
		path, err := lookup("g" + name)
		if err != nil {
			return request, err
		}
		if err := add(name, path, "/usr/local/bin/"+name, 0755); err != nil {
			return request, err
		}
	}
	for _, name := range []string{"ssh-keygen", "grep", "sed", "bsdtar", "gzip", "find"} {
		alias := name
		if name == "bsdtar" {
			alias = "tar"
		}
		path := "/usr/bin/" + name
		if err := add(alias, path, path, 0755); err != nil {
			return request, err
		}
	}
	data := filepath.Join(filepath.Dir(filepath.Dir(qemu)), "share", "qemu")
	for _, file := range []struct{ name, source, destination string }{
		{"firmware-code.fd", "edk2-aarch64-code.fd", "firmware/code.fd"},
		{"firmware-vars.fd", "edk2-arm-vars.fd", "firmware/vars.fd"},
		{"qemu-data-efi-virtio.rom", "efi-virtio.rom", "qemu-data/efi-virtio.rom"},
		{"qemu-data-vgabios-virtio.bin", "vgabios-virtio.bin", "qemu-data/vgabios-virtio.bin"},
		{"qemu-data-keymaps-en-us", "keymaps/en-us", "qemu-data/keymaps/en-us"},
	} {
		if err := add(file.name, filepath.Join(data, file.source), "/nvkr/toolchain/"+file.destination, 0444); err != nil {
			return request, err
		}
	}
	for _, name := range []string{"jq", "docker", "go", "clang"} {
		if _, err := lookup(name); err != nil {
			return request, err
		}
	}
	return request, nil
}

// These bounded metadata probes admit basic installed-command usability before
// any attempt state. NativeGo retains SDK/compiler measurement ownership; a
// successful Clang version query does not prove Apple SDK headers or linking.
func admitMacBuildPrerequisites(ctx context.Context, runner Runner, repository string) error {
	pin, err := boundedRegularRead(filepath.Join(repository, ".go-version"), 64)
	if err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "read required installed Go version")
	}
	version := strings.TrimSpace(string(pin))
	if version == "" {
		return e.New(e.CodeEngineCapability, "repository Go version pin is empty")
	}
	runner.Env = SetEnv(runner.Env, map[string]string{"GOTOOLCHAIN": "local", "GOENV": "off", "GOWORK": "off"})
	for _, probe := range []struct {
		name, phase string
		args        []string
	}{
		{"go", "native-build-go-prerequisite", []string{"env", "GOVERSION"}},
		{"docker", "native-build-buildx-prerequisite", []string{"buildx", "version"}},
		{"docker", "native-build-daemon-prerequisite", []string{"version", "--format", "{{.Server.Version}}"}},
		{"clang", "native-build-clang-prerequisite", []string{"--version"}},
	} {
		command, err := commandPath(probe.name, runner.Env)
		if err != nil {
			return e.Wrap(e.CodeEngineCapability, err, "%s unavailable", probe.phase)
		}
		raw, err := runner.Capture(ctx, append([]string{command}, probe.args...), probe.phase, 15*time.Second)
		if err != nil {
			return e.Wrap(e.CodeEngineCapability, err, "%s failed", probe.phase)
		}
		actual := strings.TrimSpace(string(raw))
		if actual == "" {
			return e.New(e.CodeEngineCapability, "%s returned no installed-tool metadata", probe.phase)
		}
		if probe.name == "go" && actual != "go"+version {
			return e.New(e.CodeEngineCapability, "installed Go must match repository .go-version (%s)", version)
		}
	}
	return nil
}

func prepareDefaultMacBuild(ctx context.Context, runner Runner, cfg *BuildConfig) error {
	runner.directory = cfg.Repository
	env, err := localBuildSourceEnvironment(runner.Env, environmentValue(runner.Env, "NVKE_LOCAL_CLONE"))
	if err != nil {
		return err
	}
	runner.Env = env
	runner.Env = SetEnv(runner.Env, map[string]string{"CHECKPOINT_DISABLE": "1"})
	if err := admitMacBuildPrerequisites(ctx, runner, cfg.Repository); err != nil {
		return err
	}
	request, err := discoverMacPackerFiles(ctx, runner, cfg.Repository)
	if err != nil {
		return err
	}
	root := filepath.Join(".nvkr", "mac-build", cfg.AttemptPrefix)
	absolute := filepath.Join(cfg.Repository, root)
	parent, leaf, err := securepath.CreateParentBeneath(cfg.Repository, root)
	if err != nil {
		return err
	}
	defer parent.Close()
	if err := unix.Mkdirat(int(parent.Fd()), leaf, 0700); err != nil {
		return err
	}
	runner.Work = absolute
	// Keep bootstrap bind sources inside the attempt's repository workspace.
	runner.Env = SetEnv(runner.Env, map[string]string{"TMPDIR": runner.Work})
	manifest := filepath.Join(root, "native-packer-files.json")
	if err := privateJSON(filepath.Join(cfg.Repository, manifest), request); err != nil {
		return err
	}
	invoke := func(args []string, phase string, timeout time.Duration, into any) error {
		raw, err := runner.Capture(ctx, args, phase, timeout)
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, into)
	}
	var bundle factory.NativePackerPreparation
	if err := invoke([]string{cfg.NVKR, "factory", "prepare-native-packer", "--manifest", manifest, "--out", filepath.Join(root, "toolchain")}, "native-packer-dependency-preparation", 0, &bundle); err != nil {
		return err
	}
	var prepared factory.NativeBuildPreparation
	if err := invoke([]string{cfg.NVKR, "factory", "prepare-native-packer-inputs", "--definition", cfg.Definition, "--environment", cfg.Environment,
		"--layout", bundle.Layout, "--bundle", bundle.Bundle, "--toolchain", bundle.Toolchain, "--out", filepath.Join(root, "packer-inputs"),
		"--accelerator", "tcg", "--variable", "cpu_model=max", "--variable", "ssh_timeout=180m", "--variable", "efi_firmware_code=/nvkr/toolchain/firmware/code.fd", "--variable", "efi_firmware_vars=/nvkr/toolchain/firmware/vars.fd"}, "native-packer-input-preparation", 0, &prepared); err != nil {
		return err
	}
	// The Linux vendoring image and Darwin execution bundle retain separate pins.
	// Follow the same admitted Plan scopes as the Linux launcher's shared jq owner.
	baseline, err := runner.Capture(ctx, []string{cfg.NVKR, "run", "plan", "--definition", prepared.Definition, "--environment", prepared.Environment, "--json"}, "native-build-baseline", 2*time.Minute)
	if err != nil {
		return err
	}
	plan := filepath.Join(root, "baseline-plan.json")
	if err := privateWrite(filepath.Join(cfg.Repository, plan), baseline); err != nil {
		return err
	}
	if _, err := factory.LoadPlanContext(ctx, filepath.Join(cfg.Repository, plan)); err != nil {
		return err
	}
	scopes, err := runner.Capture(ctx, []string{"jq", "-L", "scripts", "-e", `include "nvke-os-plan"; local_toolchain_scopes("all")`, plan}, "native-build-toolchain-scopes", time.Minute)
	if err != nil {
		return err
	}
	image, err := runner.Capture(ctx, []string{"bash", "toolchains/nvkr-packer/build.sh", "--arch", "arm64", "--diagnostic-local", "--print-validated-image-id", "--tag", "localhost/nvkr-packer-toolchain:laptop-" + cfg.AttemptPrefix}, "native-build-utility-bootstrap", 6*time.Hour)
	if err != nil {
		return err
	}
	pin := strings.TrimSpace(string(image))
	if !canonical.IsDigest(pin) {
		return e.New(e.CodeVerifyFailed, "native utility bootstrap returned no exact validated image ID")
	}
	scopePath := filepath.Join(root, "toolchain-scopes.json")
	if err := privateWrite(filepath.Join(cfg.Repository, scopePath), scopes); err != nil {
		return err
	}
	substitutions, err := runner.Capture(ctx, []string{"jq", "-L", "scripts", "-e", "--arg", "image", pin,
		`include "nvke-os-plan"; local_toolchain_substitutions($image)`, scopePath}, "native-build-toolchain-substitutions", time.Minute)
	if err != nil {
		return err
	}
	scopePath = filepath.Join(root, "substitutions.json")
	if err := privateWrite(filepath.Join(cfg.Repository, scopePath), substitutions); err != nil {
		return err
	}
	var selected []json.RawMessage
	if err := json.Unmarshal(substitutions, &selected); err != nil || len(selected) > 3 {
		return e.New(e.CodeVerifyFailed, "native utility preparation returned invalid catalog substitutions")
	}
	if len(selected) == 0 {
		cfg.Definition, cfg.Environment = prepared.Definition, prepared.Environment
		return nil
	}
	for index := range selected {
		name := "toolchain-attempt.json"
		if index != 0 {
			name = fmt.Sprintf("toolchain-attempt-%d.json", index)
		}
		raw, err := runner.Capture(ctx, []string{"jq", "-L", "scripts", "-e", "--argjson", "index", fmt.Sprint(index), "--arg", "to", pin,
			`include "nvke-os-plan"; .[$index] | toolchain_overlay("arm64"; .pin; $to; .catalogDigest; .components)`, scopePath}, fmt.Sprintf("native-build-toolchain-overlay-%d", index), time.Minute)
		if err != nil {
			return err
		}
		if err := privateWrite(filepath.Join(absolute, name), raw); err != nil {
			return err
		}
	}
	definition, err := runner.Capture(ctx, []string{"jq", "-L", "scripts", "-e", "--arg", "definition_dir", filepath.ToSlash(filepath.Dir(prepared.Definition)), "--slurpfile", "scopes", scopePath,
		`include "nvke-os-plan"; local_toolchain_definition($definition_dir; "../../../"; $scopes[0])`, prepared.Definition}, "native-build-toolchain-definition", time.Minute)
	if err != nil {
		return err
	}
	final := filepath.Join(root, "definition.json")
	if err := privateWrite(filepath.Join(cfg.Repository, final), definition); err != nil {
		return err
	}
	cfg.Definition, cfg.Environment = final, prepared.Environment
	return nil
}

// This projects catalog-validated metadata; it does not resolve tags or parse
// plugin versions. The installed Packer/plugin probes remain the runtime checks.
func macPackerPrerequisiteSelection(packer catalog.Component, plugin catalog.ToolPin) (string, string, error) {
	version := strings.TrimPrefix(packer.Tag, "v")
	if version == "" || plugin.Platform != "darwin/arm64" || plugin.ExecutablePath == "" {
		return "", "", e.New(e.CodeEngineCapability, "native Mac BUILD requires catalog-selected Packer and Darwin QEMU plugin metadata")
	}
	return version, filepath.Base(plugin.ExecutablePath), nil
}
