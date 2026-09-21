package factory

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"sigs.k8s.io/yaml"
)

// applyHelmValueOverrides mutates chartDir/values.yaml at each override's
// JSONPath with its pre-rendered value (resolveHelmValueOverrides already
// rendered every value at compile time; this step only navigates and writes).
// If checksumRoot/checksums.txt already exists (bindHelmImage produces one for
// the AICR image-bound tree), it is regenerated so it stays exact; a plain
// source-archive chart has none and none is created.
func applyHelmValueOverrides(ctx context.Context, checksumRoot, chartDir string, overrides []HelmValueOverride) error {
	if len(overrides) == 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "applying Helm values overrides")
	}
	valuesPath := filepath.Join(chartDir, "values.yaml")
	raw, err := os.ReadFile(valuesPath)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "reading Helm values")
	}
	var values map[string]any
	if err := yaml.UnmarshalStrict(raw, &values); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "decoding Helm values")
	}
	for _, override := range overrides {
		segments := strings.Split(strings.TrimPrefix(override.JSONPath, "."), ".")
		node := values
		for _, segment := range segments[:len(segments)-1] {
			next, ok := node[segment].(map[string]any)
			if !ok {
				return e.New(e.CodeVerifyFailed, "Helm values override %q: %q is not a mapping", override.JSONPath, segment)
			}
			node = next
		}
		node[segments[len(segments)-1]] = override.Value
	}
	encoded, err := yaml.Marshal(values)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "encoding Helm values")
	}
	if err := os.WriteFile(valuesPath, encoded, 0o644); err != nil {
		return e.Wrap(e.CodeInternal, err, "writing Helm values overrides")
	}
	if _, err := os.Lstat(filepath.Join(checksumRoot, "checksums.txt")); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return e.Wrap(e.CodeInternal, err, "checking Helm checksums")
	}
	return regenerateHelmChecksums(ctx, checksumRoot)
}

// regenerateHelmChecksums recomputes checksums.txt over every regular file under
// root (excluding checksums.txt itself), the same shape bindHelmImage produces,
// so an existing checksum manifest never goes stale after a values mutation.
func regenerateHelmChecksums(ctx context.Context, root string) error {
	checksumsPath := filepath.Join(root, "checksums.txt")
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || path == checksumsPath {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 8<<20 {
			return fmt.Errorf("unsupported Helm tree member %s", path)
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "admitting Helm tree for checksum regeneration")
	}
	sort.Strings(files)
	var checksums strings.Builder
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "hashing Helm tree")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "hashing Helm member")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return e.Wrap(e.CodeInternal, err, "naming Helm member")
		}
		fmt.Fprintf(&checksums, "%x  %s\n", sha256.Sum256(raw), filepath.ToSlash(relative))
	}
	if err := os.WriteFile(checksumsPath, []byte(checksums.String()), 0o644); err != nil {
		return e.Wrap(e.CodeInternal, err, "writing Helm checksums")
	}
	return nil
}
