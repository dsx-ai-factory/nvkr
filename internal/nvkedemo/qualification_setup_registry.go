// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
)

const qualificationRegistryLabel = "dev.nvkr.demo.qualification-owner"

var qualificationContainerID = regexp.MustCompile(`^[a-f0-9]{64}$`)
var qualificationOwner = regexp.MustCompile(`^[A-Z2-7]{26}$`)

type qualificationRegistry struct {
	ID    string `json:"id"`
	Owner string `json:"owner"`
	Port  string `json:"port"`
}

type qualificationRegistryInspect struct {
	ID     string `json:"Id"`
	Config struct {
		Image  string
		Labels map[string]string
	}
	State      struct{ Running bool }
	Mounts     []struct{ Type, Source, Destination string }
	HostConfig struct {
		NetworkMode    string
		ReadonlyRootfs bool
		PortBindings   map[string][]struct{ HostIP, HostPort string }
	}
	NetworkSettings struct {
		Ports map[string][]struct{ HostIP, HostPort string }
	}
}

func inspectQualificationRegistry(ctx context.Context, runner Runner, out string, registry qualificationRegistry) (qualificationRegistryInspect, error) {
	var zero qualificationRegistryInspect
	if !qualificationContainerID.MatchString(registry.ID) || !qualificationOwner.MatchString(registry.Owner) {
		return zero, configUsage("invalid qualification registry ownership description")
	}
	raw, err := runner.Capture(ctx, []string{"docker", "inspect", registry.ID}, "inspect-qualification-registry", 30*time.Second)
	if err != nil {
		return zero, err
	}
	var rows []qualificationRegistryInspect
	if err := json.Unmarshal(raw, &rows); err != nil || len(rows) != 1 {
		return zero, configUsage("require one exact qualification registry")
	}
	row := rows[0]
	if err := validateQualificationRegistry(row, out, registry); err != nil {
		return zero, err
	}
	return row, nil
}

func validateQualificationRegistry(row qualificationRegistryInspect, out string, registry qualificationRegistry) error {
	ports := row.HostConfig.PortBindings["5000/tcp"]
	if row.ID != registry.ID || row.Config.Image != registryImage || row.Config.Labels[qualificationRegistryLabel] != registry.Owner ||
		row.Config.Labels[qualificationRegistryLabel+"-root"] != out || !row.HostConfig.ReadonlyRootfs || row.HostConfig.NetworkMode != "bridge" ||
		len(row.HostConfig.PortBindings) != 1 || len(ports) != 1 || ports[0].HostIP != "127.0.0.1" ||
		len(row.Mounts) != 1 || row.Mounts[0].Type != "bind" || row.Mounts[0].Source != filepath.Join(out, "registry-data") || row.Mounts[0].Destination != "/var/lib/registry" {
		return configUsage("qualification registry ownership, image, storage or loopback binding changed")
	}
	return nil
}

func startQualificationRegistry(ctx context.Context, runner Runner, out string) (registry qualificationRegistry, result error) {
	registry.Owner = rand.Text()
	if err := runner.Run(ctx, []string{"docker", "pull", registryImage}, "pull-qualification-registry", 5*time.Minute); err != nil {
		return registry, err
	}
	// Persist the owner before Docker creation. The cidfile also survives a lost
	// create response, so a cancellation cannot silently orphan a running service.
	if err := privateWrite(filepath.Join(out, "registry-owner"), []byte(registry.Owner)); err != nil {
		return registry, err
	}
	cidfile := filepath.Join(out, "registry.cid")
	_, createErr := runner.Capture(ctx, []string{"docker", "create", "--cidfile", cidfile,
		"--label", qualificationRegistryLabel + "=" + registry.Owner, "--label", qualificationRegistryLabel + "-root=" + out,
		"--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--network", "bridge", "--publish", "127.0.0.1::5000",
		"--mount", "type=bind,src=" + filepath.Join(out, "registry-data") + ",dst=/var/lib/registry", registryImage}, "create-qualification-registry", time.Minute)
	raw, readErr := boundedRegularRead(cidfile, 128)
	if readErr != nil {
		return registry, errors.Join(createErr, readErr)
	}
	registry.ID = strings.TrimSpace(string(raw))
	if !qualificationContainerID.MatchString(registry.ID) {
		return registry, configUsage("Docker returned an invalid registry container ID")
	}
	defer func() {
		if result != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			result = errors.Join(result, stopQualificationRegistry(cleanup, Runner{Env: NativeEnvironment()}, out, registry))
		}
	}()
	state, err := canonical.Marshal(registry)
	if err != nil {
		return registry, err
	}
	if err := privateWrite(filepath.Join(out, "registry.json"), state); err != nil {
		return registry, err
	}
	if createErr != nil {
		return registry, createErr
	}
	if err := runner.Run(ctx, []string{"docker", "start", registry.ID}, "start-qualification-registry", time.Minute); err != nil {
		return registry, err
	}
	row, err := inspectQualificationRegistry(ctx, Runner{Env: runner.Env}, out, registry)
	if err != nil {
		return registry, err
	}
	ports := row.NetworkSettings.Ports["5000/tcp"]
	if !row.State.Running || len(ports) != 1 || ports[0].HostIP != "127.0.0.1" {
		return registry, configUsage("qualification registry did not acquire one loopback port")
	}
	port, err := strconv.Atoi(ports[0].HostPort)
	if err != nil || port < 1024 || port > 65535 {
		return registry, configUsage("invalid qualification registry port")
	}
	registry.Port = strconv.Itoa(port)
	state, err = canonical.Marshal(registry)
	if err != nil {
		return registry, err
	}
	if err := privateReplace(filepath.Join(out, "registry.json"), state); err != nil {
		return registry, err
	}
	return registry, waitQualificationRegistry(ctx, registry.Port)
}

func waitQualificationRegistry(ctx context.Context, port string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:"+port+"/v2/", nil)
		if err != nil {
			return err
		}
		resp, err := client.Do(req)
		if err == nil {
			closeErr := resp.Body.Close()
			if resp.StatusCode == http.StatusOK && resp.Header.Get("Docker-Distribution-API-Version") == "registry/2.0" {
				return closeErr
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("qualification registry readiness: %w", ctx.Err())
		case <-time.After(time.Second):
		}
	}
}

func stopQualificationRegistry(ctx context.Context, runner Runner, out string, registry qualificationRegistry) error {
	row, err := inspectQualificationRegistry(ctx, runner, out, registry)
	if err != nil {
		return err
	}
	if !row.State.Running {
		return nil
	}
	if err := runner.Run(ctx, []string{"docker", "stop", "--time", "10", registry.ID}, "stop-qualification-registry", 30*time.Second); err != nil {
		return err
	}
	row, err = inspectQualificationRegistry(ctx, runner, out, registry)
	if err != nil {
		return err
	}
	if row.State.Running {
		return configUsage("qualification registry remains running")
	}
	return nil
}
