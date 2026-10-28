// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

const managementPodCIDR = "10.244.0.0/16"
const managementServiceCIDR = "10.96.0.0/16"

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

// Bind the exact admitted carrier index as well as its Run-selected child.
func diagnosticIndexDigest(ctx context.Context, layout, digest string) (string, error) {
	if err := requireContext(ctx); err != nil {
		return "", err
	}
	parent, err := os.Lstat(filepath.Dir(layout))
	if err != nil {
		return "", err
	}
	if !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("producer parent is not a regular directory")
	}
	if err := regularTree(layout); err != nil {
		return "", err
	}
	raw, err := boundedRegularRead(filepath.Join(layout, "index.json"), diagnosticLimit)
	if err != nil {
		return "", err
	}
	var index v1.IndexManifest
	if err = json.Unmarshal(raw, &index); err != nil {
		return "", err
	}
	if len(index.Manifests) != 1 || index.Manifests[0].Digest.String() != digest {
		return "", errors.New("selected layout differs from completed Run manifest")
	}
	return "sha256:" + plainSHA(raw), nil
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

func plainSHA(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

// ManagementSession is a live diagnostic lifetime, not an NVKR receipt.
type ManagementSession struct {
	Name, Tools, Work string
	Runner            Runner
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
		if !info.Mode().IsRegular() {
			return fmt.Errorf("require regular tool member: %s", row.Name())
		}
		if row.Name() == "nvke-installer-sources" {
			if info.Mode().Perm() != nativeToolMemberMode(row.Name()) {
				return errors.New("require regular nonexecutable installer source archive")
			}
		} else if info.Mode().Perm()&0o111 == 0 {
			return fmt.Errorf("require executable tool member: %s", row.Name())
		}
		names = append(names, row.Name())
	}
	if !slices.Equal(names, members) {
		return errors.New("unexpected executable tool membership")
	}
	return nil
}
