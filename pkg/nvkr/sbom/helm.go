// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sbom

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/url"
	"path"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/helmarchive"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"sigs.k8s.io/yaml"
)

// Helm charts declare dependencies which are not necessarily vendored. Record
// references as references; never execute install scripts or resolve a mutable
// version over the network to fill out the inventory.
func inspectHelmArchive(ctx context.Context, source io.Reader) ([]Component, error) {
	buffered := bufio.NewReader(source)
	var expanded io.Reader = buffered
	if magic, _ := buffered.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		compressed, err := gzip.NewReader(buffered)
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "opening compressed Helm archive")
		}
		defer func() { _ = compressed.Close() }()
		expanded = compressed
	}
	limited := &io.LimitedReader{R: expanded, N: (256 << 20) + 1}
	reader := tar.NewReader(limited)
	var components []Component
	seen := helmarchive.Records{}
	for entries := 0; ; entries++ {
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "inspecting Helm archive")
		}
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "reading Helm archive")
		}
		name := strings.TrimSuffix(header.Name, "/")
		if entries >= 65536 || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") {
			return nil, e.New(e.CodeVerifyFailed, "unsafe or duplicate Helm archive member %q", name)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir {
			return nil, e.New(e.CodeVerifyFailed, "unsupported Helm archive member %q", name)
		}
		base := path.Base(name)
		metadata := header.Typeflag == tar.TypeReg && (base == "Chart.yaml" || base == "Chart.lock")
		if header.Size < 0 || (metadata && header.Size > 8<<20) {
			return nil, e.New(e.CodeVerifyFailed, "Helm dependency document is oversized")
		}
		hash := sha256.New()
		var raw []byte
		if metadata {
			raw, err = io.ReadAll(io.TeeReader(reader, hash))
		} else {
			_, err = io.Copy(hash, reader)
		}
		if err != nil || limited.N <= 0 {
			return nil, e.New(e.CodeVerifyFailed, "Helm archive is truncated or exceeds byte limit: %v", err)
		}
		var digest [sha256.Size]byte
		copy(digest[:], hash.Sum(nil))
		repeated, err := seen.Add(strings.Split(name, "/")[0], header, digest)
		if err != nil {
			return nil, err
		}
		if repeated || !metadata {
			continue
		}
		decoded, err := yaml.YAMLToJSONStrict(raw)
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding Helm dependency document %s", name)
		}
		var chart struct {
			Name         string `json:"name"`
			Version      string `json:"version"`
			Dependencies []struct {
				Name       string `json:"name"`
				Version    string `json:"version"`
				Repository string `json:"repository"`
			} `json:"dependencies"`
		}
		if err := json.Unmarshal(decoded, &chart); err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "reading chart dependency metadata")
		}
		if base == "Chart.yaml" {
			component, err := helmComponent(chart.Name, chart.Version, "", "bundled-chart")
			if err != nil {
				return nil, err
			}
			components = append(components, component)
		}
		for _, dep := range chart.Dependencies {
			scope := "declared-reference"
			if base == "Chart.lock" {
				scope = "locked-reference"
			}
			component, err := helmComponent(dep.Name, dep.Version, dep.Repository, scope)
			if err != nil {
				return nil, err
			}
			components = append(components, component)
		}
	}
	if limited.N == 0 {
		return nil, e.New(e.CodeVerifyFailed, "expanded Helm archive exceeds size limit")
	}
	if len(components) == 0 {
		return nil, e.New(e.CodeProvenanceMissing, "archive contains no supported Helm dependency metadata")
	}
	return components, nil
}

func helmComponent(name, version, repository, scope string) (Component, error) {
	if !canonicalText(name) || !canonicalText(version) {
		return Component{}, e.New(e.CodeProvenanceMissing, "chart dependency requires an explicit name and version")
	}
	purl := "pkg:helm/" + url.PathEscape(name) + "@" + url.PathEscape(version)
	if repository != "" {
		purl += "?repository_url=" + url.QueryEscape(repository)
	}
	return Component{Type: "application", Name: name, Version: version, PURL: purl, Properties: []Property{{Name: "nvkr:" + scope, Value: "true"}}}, nil
}
