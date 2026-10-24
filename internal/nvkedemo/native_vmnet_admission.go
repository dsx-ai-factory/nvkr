// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// NICoSim's selected Darwin backend uses installed paths rather than PATH or
// the extracted bundle. Admit the same first candidate, and the fixed helper
// invoked by the selected client's compiled prefix, before creating a bridge.
func admitNativeVMNet(ctx context.Context, platform, tools string) error {
	if ctx == nil {
		return nvkrerr.New(nvkrerr.CodeUsage, "vmnet admission requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "vmnet admission context")
	}
	if platform != "darwin" {
		return nil
	}
	err := admitNativeVMNetPair(ctx, tools, []string{
		"/opt/vmnet-helper/bin/vmnet-client",
		"/opt/homebrew/opt/vmnet-helper/libexec/vmnet-client",
		"/usr/local/opt/vmnet-helper/libexec/vmnet-client",
	}, "/opt/vmnet-helper/bin/vmnet-helper")
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nvkrerr.Wrap(nvkrerr.CodeTimeout, err, "vmnet admission context")
	}
	if err != nil {
		return nvkrerr.Wrap(nvkrerr.CodeEngineCapability, err, "installed vmnet pair must match the selected native tool bundle")
	}
	return nil
}

func admitNativeVMNetPair(ctx context.Context, tools string, candidates []string, helper string) error {
	if err := requireContext(ctx); err != nil {
		return err
	}
	client := ""
	for _, candidate := range candidates {
		// This selection order intentionally matches NICoSim. Do not skip a
		// mismatched first candidate in favor of a later matching executable.
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			client = candidate
			break
		}
	}
	if client == "" {
		return errors.New("selected NICoSim installed vmnet-client is missing")
	}
	for _, member := range []struct{ name, installed string }{{"vmnet-client", client}, {"vmnet-helper", helper}} {
		path, err := admitNetworkProbeExecutable(ctx, member.installed)
		if err != nil {
			return err
		}
		selected := filepath.Join(tools, member.name)
		installedInfo, err := os.Stat(path)
		if err != nil {
			return err
		}
		selectedInfo, err := os.Lstat(selected)
		if err != nil {
			return err
		}
		if !selectedInfo.Mode().IsRegular() || selectedInfo.Size() > 256<<20 || installedInfo.Size() != selectedInfo.Size() {
			return errors.New("installed " + member.name + " has a different or oversized executable length")
		}
		// The existing exact-blob owner holds a no-follow descriptor and checks
		// metadata after hashing. This remains trusted-host path admission,
		// not immutable execution authority against root or the current user.
		actual, err := DigestFile(ctx, path)
		if err != nil {
			return err
		}
		expected, err := DigestFile(ctx, selected)
		if err != nil {
			return err
		}
		if actual != expected {
			return errors.New("installed " + member.name + " differs from its selected native bundle member")
		}
	}
	return nil
}
