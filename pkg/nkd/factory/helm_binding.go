package factory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"sigs.k8s.io/yaml"
)

// bindHelmDeployment changes only the unique image scalar of a single-container
// rendered Deployment. It preserves the upstream document and license comments.
// Multi-container or already-templated inputs require a different explicit adapter.
func bindHelmDeployment(raw []byte, digest string) ([]byte, error) {
	if !canonical.IsDigest(digest) || bytes.Contains(raw, []byte("{{")) {
		return nil, e.New(e.CodeVerifyFailed, "Helm image binding requires a digest and plain rendered YAML")
	}
	var oldImage string
	deployments := 0
	separator := regexp.MustCompile(`(?m)^---[ \t]*(?:#[^\r\n]*)?\r?$`)
	for _, document := range separator.Split(string(raw), -1) {
		var object map[string]any
		if err := yaml.UnmarshalStrict([]byte(document), &object); err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "reading rendered Helm deployment")
		}
		if object["kind"] != "Deployment" {
			continue
		}
		deployments++
		value := any(object)
		for _, key := range []string{"spec", "template", "spec", "containers"} {
			mapping, ok := value.(map[string]any)
			if !ok {
				return nil, e.New(e.CodeVerifyFailed, "Helm Deployment has no containers")
			}
			value = mapping[key]
		}
		containers, ok := value.([]any)
		if !ok || len(containers) != 1 {
			return nil, e.New(e.CodeVerifyFailed, "Helm image binding requires one Deployment container")
		}
		container, ok := containers[0].(map[string]any)
		if !ok {
			return nil, e.New(e.CodeVerifyFailed, "Helm Deployment container is malformed")
		}
		oldImage, ok = container["image"].(string)
		if !ok || oldImage == "" {
			return nil, e.New(e.CodeVerifyFailed, "Helm Deployment has no image")
		}
	}
	if deployments != 1 {
		return nil, e.New(e.CodeVerifyFailed, "Helm image binding requires one Deployment")
	}
	image := regexp.QuoteMeta(oldImage)
	line := regexp.MustCompile(`(?m)^([ \t]*image:[ \t]*)(?:"` + image + `"|'` + image + `'|` + image + `)[ \t]*$`)
	if len(line.FindAllIndex(raw, -1)) != 1 {
		return nil, e.New(e.CodeVerifyFailed, "Helm image scalar is ambiguous or unsupported")
	}
	expression := `{{ printf "%s@` + digest + `" (required "set image.repository to the verified CWE Release repository" .Values.image.repository) | quote }}`
	return line.ReplaceAllFunc(raw, func(match []byte) []byte {
		prefix := match[:bytes.IndexByte(match, ':')+1]
		return append(append(append([]byte(nil), prefix...), ' '), expression...)
	}), nil
}

// rejectBoundImageOverride refuses the image subtree of an image-bound chart:
// the digest is verified and the repository must stay a deploy-time input so
// chart bytes remain portable across registry mirrors.
func rejectBoundImageOverride(path string) error {
	if path == "image" || strings.HasPrefix(path, "image.") {
		return e.New(e.CodeInvalidCatalog, "Helm values cannot override the verified image binding %q", path)
	}
	return nil
}

// bindHelmImage operates only on the private extracted source tree. The caller
// derives digest from the verified image package in the same compiled graph.
func bindHelmImage(ctx context.Context, root, chart, digest string, overrides []HelmValueOverride) error {
	if !canonicalName.MatchString(chart) || !canonical.IsDigest(digest) {
		return e.New(e.CodeInvalidCatalog, "Helm image binding requires a chart directory and image digest")
	}
	for _, override := range overrides {
		if err := rejectBoundImageOverride(override.Path); err != nil {
			return err
		}
	}
	// Reject links before any mutation, including contained aliases that could
	// make one selected file overwrite another member of the source bundle.
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 8<<20 {
			return fmt.Errorf("unsupported Helm source member %s", path)
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "admitting Helm binding tree")
	}
	deployment := filepath.Join(root, chart, "templates", "controller.yaml")
	raw, err := os.ReadFile(deployment)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "reading Helm Deployment")
	}
	bound, err := bindHelmDeployment(raw, digest)
	if err != nil {
		return err
	}
	valuesPath := filepath.Join(root, chart, "values.yaml")
	raw, err = os.ReadFile(valuesPath)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "reading Helm values")
	}
	var values map[string]any
	if err := yaml.UnmarshalStrict(raw, &values); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "decoding Helm values")
	}
	image, ok := values["image"].(map[string]any)
	if !ok {
		return e.New(e.CodeVerifyFailed, "Helm values have no image mapping")
	}
	image["digest"], image["repository"] = digest, ""
	delete(image, "tag")
	if err := setHelmValues(values, overrides); err != nil {
		return err
	}
	encoded, err := yaml.Marshal(values)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "encoding Helm values")
	}
	if err := os.WriteFile(deployment, bound, 0o644); err != nil {
		return e.Wrap(e.CodeInternal, err, "binding Helm Deployment")
	}
	if err := os.WriteFile(valuesPath, encoded, 0o644); err != nil {
		return e.Wrap(e.CodeInternal, err, "binding Helm values")
	}
	var checksums strings.Builder
	for _, path := range files {
		if path == filepath.Join(root, "checksums.txt") {
			continue
		}
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "hashing bound Helm tree")
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
	if err := os.WriteFile(filepath.Join(root, "checksums.txt"), []byte(checksums.String()), 0o644); err != nil {
		return e.Wrap(e.CodeInternal, err, "writing Helm checksums")
	}
	return nil
}
