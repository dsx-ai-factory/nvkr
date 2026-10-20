// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// ChartInputsConfig describes a downstream chart handoff, not a Release.
type ChartInputsConfig struct {
	NVKECheckout, NVKR, Out     string
	Charts, Manifests, Versions map[string]string
	Progress                    io.Writer
}
type chartCapture func(context.Context, []string, string, time.Duration) ([]byte, error)

func verifyInstaller(ctx context.Context, runner Runner, checkout string) error {
	head, err := runner.Capture(ctx, []string{"git", "-C", checkout, "rev-parse", "HEAD"}, "installer-pin", 30*time.Second)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(head)) != InstallerCommit {
		return errors.New("require exact selected NVKE installer commit")
	}
	dirty, err := runner.Capture(ctx, []string{"git", "-C", checkout, "status", "--porcelain", "--untracked-files=all"}, "clean-installer", 30*time.Second)
	if err != nil {
		return err
	}
	if len(dirty) > 0 {
		return errors.New("NVKE installer checkout must be clean")
	}
	return nil
}

// releaseFields asks the pinned NVKE reader to validate its own format. The
// unit-separator result is that reader's exact existing CLI interface, not YAML.
func releaseFields(ctx context.Context, capture chartCapture, reader, path string) ([]string, error) {
	raw, err := capture(ctx, []string{"python3", reader, "release", path}, "release-fields", 30*time.Second)
	if err != nil {
		return nil, err
	}
	fields := strings.Split(strings.TrimRight(string(raw), "\n"), "\x1f")
	if len(fields) != 13 || fields[0] != "single" {
		return nil, errors.New("NVKE reader returned unsupported deploy-item interface")
	}
	return fields, nil
}

func projectChartSource(raw []byte, archive string) ([]byte, error) {
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	source := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "  path: ") || strings.HasPrefix(line, "  repository: ") {
			if source != -1 {
				return nil, errors.New("ambiguous NVKE chart source")
			}
			source = i
		}
	}
	if source < 0 {
		return nil, errors.New("NVKE chart source differs from inspected single-source form")
	}
	lines[source] = "  path: " + archive
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

func prepareChartInputs(ctx context.Context, cfg ChartInputsConfig, capture chartCapture) (deploy string, result error) {
	if err := requireContext(ctx); err != nil {
		return "", err
	}
	checkout, err := resolveExisting(cfg.NVKECheckout)
	if err != nil {
		return "", err
	}
	tool, err := resolveExisting(cfg.NVKR)
	if err != nil {
		return "", err
	}
	destination, err := freshDestination(cfg.Out)
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(destination)
	if insidePath(parent, checkout) {
		return "", errors.New("prepared inputs must be outside NVKE checkout")
	}
	head, err := capture(ctx, []string{"git", "-C", checkout, "rev-parse", "HEAD"}, "chart-source-pin", 30*time.Second)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(head)) != InstallerCommit {
		return "", errors.New("NVKE checkout differs from selected installer commit")
	}
	dirty, err := capture(ctx, []string{"git", "-C", checkout, "status", "--porcelain", "--untracked-files=all"}, "chart-clean-source", 30*time.Second)
	if err != nil {
		return "", err
	}
	if len(dirty) > 0 {
		return "", errors.New("NVKE chart installer checkout must be clean")
	}
	reader := filepath.Join(checkout, installerDeployReader)
	pinnedReader, err := capture(ctx, []string{"git", "-C", checkout, "show", InstallerCommit + ":" + installerDeployReader}, "chart-pinned-reader", 30*time.Second)
	if err != nil {
		return "", err
	}
	actualReader, err := boundedRegularRead(reader, diagnosticLimit)
	if err != nil {
		return "", err
	}
	if !bytes.Equal(pinnedReader, actualReader) {
		return "", errors.New("NVKE reader differs from pinned Git bytes")
	}
	layouts := make(map[string]string)
	for item, path := range cfg.Charts {
		if item == "" || filepath.Base(item) != item || item == "." || item == ".." {
			return "", errors.New("chart items must be simple names")
		}
		layout, readErr := resolveExisting(path)
		if readErr != nil {
			return "", readErr
		}
		if insidePath(parent, layout) {
			return "", errors.New("prepared inputs must be outside every producer layout")
		}
		layouts[item] = layout
	}
	tracked, err := capture(ctx, []string{"git", "-C", checkout, "ls-tree", "-rz", InstallerCommit, "--", "deploy/nico"}, "chart-source-membership", 30*time.Second)
	if err != nil {
		return "", err
	}
	sources := make(map[string][]byte)
	for _, entry := range strings.Split(string(tracked), "\x00") {
		if entry == "" {
			continue
		}
		info, path, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(info)
		if !ok || len(fields) != 3 || fields[1] != "blob" || !slices.Contains([]string{"100644", "100755"}, fields[0]) {
			return "", errors.New("NVKE deploy copy requires tracked regular files")
		}
		relative, relErr := filepath.Rel("deploy/nico", path)
		if relErr != nil || !insidePath(path, "deploy/nico") || relative == "." {
			return "", errors.New("NVKE deploy file escapes source directory")
		}
		raw, readErr := capture(ctx, []string{"git", "-C", checkout, "show", InstallerCommit + ":" + path}, "chart-pinned-source", 30*time.Second)
		if readErr != nil {
			return "", readErr
		}
		if len(raw) > diagnosticLimit {
			return "", errors.New("deploy source exceeds8 MiB bound")
		}
		if _, exists := sources[relative]; exists {
			return "", errors.New("duplicate tracked deploy source")
		}
		sources[relative] = raw
	}
	temporary, err := os.MkdirTemp(parent, ".nvkr-chart-inputs-")
	if err != nil {
		return "", err
	}
	defer func() {
		if !errors.Is(result, ErrUnjoined) {
			result = errors.Join(result, os.RemoveAll(temporary))
		}
	}()
	stage := filepath.Join(temporary, "deploy")
	if err = os.Mkdir(stage, 0o700); err != nil {
		return "", err
	}
	paths := make([]string, 0, len(sources))
	for path := range sources {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	for _, path := range paths {
		target := filepath.Join(stage, path)
		if err = os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return "", err
		}
		if err = privateWrite(target, sources[path]); err != nil {
			return "", err
		}
	}
	listed, err := capture(ctx, []string{"python3", reader, "items", filepath.Join(stage, "nvke-deploy.yaml")}, "chart-items", 30*time.Second)
	if err != nil {
		return "", err
	}
	helm := make(map[string]bool)
	all := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSuffix(string(listed), "\n"), "\n") {
		fields := strings.Split(line, "\t")
		if len(fields) != 2 {
			return "", errors.New("unsupported NVKE item listing")
		}
		name := fields[0]
		if name == "" || name == "." || name == ".." || filepath.Base(name) != name || all[name] {
			return "", errors.New("NVKE deploy items require unique simple names")
		}
		all[name] = true
		if fields[1] == "helm" {
			helm[name] = true
		}
	}
	if len(helm) != len(cfg.Charts) || len(helm) != len(cfg.Manifests) {
		return "", errors.New("supply exactly one layout and manifest for every NVKE Helm item")
	}
	names := make([]string, 0, len(helm))
	for item := range helm {
		if layouts[item] == "" || cfg.Manifests[item] == "" {
			return "", errors.New("missing selected chart layout or pin")
		}
		names = append(names, item)
	}
	slices.Sort(names)
	originals := make(map[string][]string)
	for _, item := range names {
		fields, readErr := releaseFields(ctx, capture, reader, filepath.Join(stage, item, "release.yaml"))
		if readErr != nil {
			return "", readErr
		}
		originals[item] = fields
	}
	if err = os.Mkdir(filepath.Join(stage, "charts"), 0o700); err != nil {
		return "", err
	}
	for _, item := range names {
		fields := originals[item]
		archive := "charts/" + item + ".tgz"
		version := fields[2]
		if selected := cfg.Versions[item]; selected != "" {
			if version != "" && selected != version {
				return "", errors.New("selected chart version differs from pinned deploy item")
			}
			version = selected
		}
		if version == "" {
			return "", errors.New("selected local chart requires its observed version")
		}
		observation, readErr := capture(ctx, []string{tool, "publish", "chart", "--layout", layouts[item], "--manifest", cfg.Manifests[item], "--name", fields[1], "--version", version, "--out", filepath.Join(stage, archive)}, "chart-extract", 120*time.Second)
		if readErr != nil {
			return "", readErr
		}
		if !json.Valid(observation) {
			return "", errors.New("invalid nvkr chart extraction observation")
		}
		release := filepath.Join(stage, item, "release.yaml")
		raw, readErr := boundedRegularRead(release, diagnosticLimit)
		if readErr != nil {
			return "", readErr
		}
		projected, readErr := projectChartSource(raw, archive)
		if readErr != nil {
			return "", readErr
		}
		if err = privateReplace(release, projected); err != nil {
			return "", err
		}
		expected := slices.Clone(fields)
		expected[3], expected[4] = "", archive
		after, readErr := releaseFields(ctx, capture, reader, release)
		if readErr != nil {
			return "", readErr
		}
		if !slices.Equal(after, expected) {
			return "", errors.New("prepared chart changed other NVKE deploy semantics")
		}
	}
	if err = os.Mkdir(destination, 0o700); err != nil {
		return "", err
	}
	if err = os.Rename(stage, filepath.Join(destination, "deploy")); err != nil {
		return "", errors.Join(err, os.Remove(destination))
	}
	return filepath.Join(destination, "deploy"), nil
}

func sequencedChartCapture(runner Runner) chartCapture {
	sequence := 0
	return func(ctx context.Context, args []string, _ string, timeout time.Duration) ([]byte, error) {
		sequence++
		return runner.Capture(ctx, args, fmt.Sprintf("chart-input-%03d", sequence), timeout)
	}
}
