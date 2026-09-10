package sbom

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"path"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"sigs.k8s.io/yaml"
)

// Helm charts and AICR exports declare dependencies which are not necessarily
// vendored. Record references as references; never execute install scripts or
// resolve a mutable version over the network to fill out the inventory.
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
	seen := map[string]bool{}
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
		if entries >= 65536 || path.IsAbs(name) || path.Clean(name) != name || strings.HasPrefix(name, "../") || seen[name] {
			return nil, e.New(e.CodeVerifyFailed, "unsafe or duplicate Helm archive member %q", name)
		}
		seen[name] = true
		if header.Typeflag == tar.TypeDir {
			continue
		}
		if header.Typeflag != tar.TypeReg {
			return nil, e.New(e.CodeVerifyFailed, "unsupported Helm archive member %q", name)
		}
		base := path.Base(name)
		if base != "Chart.yaml" && base != "Chart.lock" && base != "recipe.yaml" {
			continue
		}
		if header.Size > 8<<20 {
			return nil, e.New(e.CodeVerifyFailed, "Helm dependency document is oversized")
		}
		raw, err := io.ReadAll(reader)
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "reading Helm dependency document")
		}
		decoded, err := yaml.YAMLToJSONStrict(raw)
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding Helm dependency document %s", name)
		}
		if base == "recipe.yaml" {
			var recipe struct {
				APIVersion    string `json:"apiVersion"`
				ComponentRefs []struct {
					Type    string `json:"type"`
					Name    string `json:"name"`
					Chart   string `json:"chart"`
					Source  string `json:"source"`
					Version string `json:"version"`
				} `json:"componentRefs"`
			}
			if err := json.Unmarshal(decoded, &recipe); err != nil {
				return nil, e.Wrap(e.CodeVerifyFailed, err, "reading deployment references")
			}
			if !strings.HasPrefix(recipe.APIVersion, "aicr.run/") {
				continue
			}
			for _, ref := range recipe.ComponentRefs {
				if ref.Type != "Helm" || ref.Chart == "" {
					continue
				}
				component, err := helmComponent(ref.Chart, ref.Version, ref.Source, "declared-reference")
				if err != nil {
					return nil, err
				}
				components = append(components, component)
			}
			continue
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
	return Component{Type: "application", Name: name, Version: version, PURL: purl, Properties: []Property{{Name: "nkd:" + scope, Value: "true"}}}, nil
}
