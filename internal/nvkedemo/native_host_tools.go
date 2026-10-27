// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Native dependency IDs differ where upstream assets have different platforms.
// Only this private runtime view uses conventional command names; the original
// authenticated extraction, bundle entries and immutable Run stay unchanged.
func extractNativeHostTools(ctx context.Context, bundle delivery.BinaryBundle, publication, out, platform string) (result error) {
	defer func() {
		if result != nil {
			result = nvkrerr.Wrap(nvkrerr.CodeVerifyFailed, result, "prepare native host tool view")
		}
	}()
	if err := requireContext(ctx); err != nil {
		return err
	}
	_, _, name, members, err := nativeToolSelection(platform)
	if err != nil {
		return err
	}
	bindings, err := nativeHostToolBindings(bundle, platform, members)
	if err != nil {
		return err
	}
	// Both destinations must be fresh before extraction. Callers separately
	// admit the workspace and its separation from immutable input Runs.
	if _, err := freshDestination(out); err != nil {
		return err
	}
	original := out + "-original"
	if _, err := freshDestination(original); err != nil {
		return err
	}
	extracted, err := delivery.ExtractBinaryBundle(ctx, publication, name, original)
	if err != nil {
		return err
	}
	expected, err := bundle.Digest()
	if err != nil {
		return err
	}
	actual, err := extracted.Digest()
	if err != nil || actual != expected {
		return errors.Join(err, errors.New("extracted host tools differ from the selected bundle"))
	}
	names := make([]string, 0, len(bindings))
	for _, entry := range bindings {
		names = append(names, entry.Name)
	}
	slices.Sort(names)
	if err := verifyToolMembers(original, names); err != nil {
		return err
	}
	if err := os.Mkdir(out, 0700); err != nil {
		return err
	}
	for _, member := range members {
		entry := bindings[member]
		destination := filepath.Join(out, member)
		if err := copyPrivate(ctx, filepath.Join(original, entry.Name), destination, os.FileMode(entry.Mode)); err != nil {
			return err
		}
		digest, err := DigestFile(ctx, destination)
		if err != nil || digest != entry.SHA256 {
			return errors.Join(err, errors.New("native tool view differs from the verified entry"))
		}
	}
	return verifyToolMembers(out, members)
}

func nativeHostToolBindings(bundle delivery.BinaryBundle, platform string, members []string) (map[string]delivery.BinaryEntry, error) {
	if bundle.OS+"/"+bundle.Arch != platform || len(bundle.Entries) != len(members) {
		return nil, errors.New("native host tool bundle has the wrong platform or membership")
	}
	bindings := make(map[string]delivery.BinaryEntry, len(members))
	for _, entry := range bundle.Entries {
		member := entry.Name
		if platform == "linux/arm64" {
			switch member {
			case "clusterctl-linux", "helm-linux", "kind-linux", "kubectl-linux", "oras-linux":
				member = member[:len(member)-len("-linux")]
			}
		}
		if !slices.Contains(members, member) || bindings[member].Name != "" {
			return nil, errors.New("native tool aliases contain an unexpected or colliding member")
		}
		if os.FileMode(entry.Mode) != nativeToolMemberMode(member) {
			return nil, errors.New("native tool bundle member has an unexpected delivered mode: " + member)
		}
		bindings[member] = entry
	}
	return bindings, nil
}

// The installer source archive is data consumed by the archive owner, never a
// host command. Keep its delivered mode in both the original and alias view.
func nativeToolMemberMode(member string) os.FileMode {
	if member == "nvke-installer-sources" {
		return 0644
	}
	return 0755
}
