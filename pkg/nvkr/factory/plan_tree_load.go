// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

const maxPlanTreeBytes = 64 << 20

func joinPlanTree(ctx context.Context, planPath string, files map[string][]byte) (*Plan, []byte, error) {
	var root planFields
	if err := decodeTreeJSON(files[planPath], &root); err != nil {
		return nil, nil, err
	}
	if string(root["storage"]) != `"`+planTreeStorage+`"` {
		return nil, nil, e.New(e.CodePipelineDrift, "unknown Plan storage format")
	}
	delete(root, "storage")
	if err := treePackageSnapshots(root, files, true); err != nil {
		return nil, nil, err
	}
	var catalogs planFields
	if err := json.Unmarshal(root["catalogSnapshots"], &catalogs); err != nil {
		return nil, nil, e.Wrap(e.CodePipelineDrift, err, "reading Plan snapshots")
	}
	delete(root, "catalogSnapshots")
	catalogBytes := 0
	for _, digest := range sortedKeys(catalogs) {
		value, err := treeCatalogRecords(catalogs[digest], files, true)
		if err != nil {
			return nil, nil, err
		}
		catalogBytes += len(value)
		if int64(catalogBytes) > MaxPlanBytes {
			return nil, nil, e.New(e.CodePipelineDrift, "catalog snapshots exceed decoded size bound")
		}
		catalogs[digest] = value
	}
	fieldBytes := 0
	for _, field := range sharedPlanFields {
		if ref, ok := root[field]; ok {
			value, err := readTreeJSON(ref, files)
			if err != nil {
				return nil, nil, err
			}
			fieldBytes += len(value)
			if int64(fieldBytes) > MaxPlanBytes {
				return nil, nil, e.New(e.CodePipelineDrift, "Plan fields exceed decoded size bound")
			}
			root[field] = value
		}
	}
	restoreCatalog := func(record planFields) error {
		var digest string
		if err := json.Unmarshal(record["catalogDigest"], &digest); err != nil {
			return e.Wrap(e.CodePipelineDrift, err, "reading catalogDigest")
		}
		snapshot, ok := catalogs[digest]
		if !ok {
			return e.New(e.CodePipelineDrift, "missing Plan snapshot")
		}
		if record["catalogSnapshot"] != nil {
			return e.New(e.CodePipelineDrift, "duplicate Plan snapshot")
		}
		record["catalogSnapshot"] = snapshot
		return nil
	}
	if err := restoreCatalog(root); err != nil {
		return nil, nil, err
	}
	if err := mapTreeCatalogCandidates(ctx, root, restoreCatalog); err != nil {
		return nil, nil, err
	}
	var builds []planFields
	if err := json.Unmarshal(root["builds"], &builds); err != nil {
		return nil, nil, e.Wrap(e.CodePipelineDrift, err, "reading Plan builds")
	}
	delete(root, "builds")
	// Bound expansion before accumulating repeated snapshots or commands.
	base, err := canonical.Marshal(root)
	if err != nil {
		return nil, nil, err
	}
	size := len(base) + len(`,"builds":[]`)
	for index, build := range builds {
		if err := ctx.Err(); err != nil {
			return nil, nil, e.Wrap(e.CodeTimeout, err, "joining Plan canceled")
		}
		if _, err := treeTargetName(build["name"]); err != nil {
			return nil, nil, err
		}
		var stepPath string
		if err := json.Unmarshal(build["file"], &stepPath); err != nil || !isPlanStoreRecord(stepPath) {
			return nil, nil, e.New(e.CodePipelineDrift, "invalid Plan step reference")
		}
		delete(build, "file")
		var step planFields
		if err := decodeTreeJSON(files[stepPath], &step); err != nil {
			return nil, nil, err
		}
		if !bytes.Equal(step["name"], build["name"]) {
			return nil, nil, e.New(e.CodePipelineDrift, "Plan step name differs from its root record")
		}
		if resolved, ok := step["resolvedComponents"]; ok {
			value, err := treeResolvedRecords(resolved, files, true)
			if err != nil {
				return nil, nil, err
			}
			step["resolvedComponents"] = value
		}
		if err := expandTreeCommands(step, files, MaxPlanBytes-int64(size)); err != nil {
			return nil, nil, err
		}
		for field, value := range step {
			if field == "name" {
				continue
			}
			if _, duplicate := build[field]; duplicate {
				return nil, nil, e.New(e.CodePipelineDrift, "Plan field %q has two owners", field)
			}
			build[field] = value
		}
		if err := restoreCatalog(build); err != nil {
			return nil, nil, err
		}
		joined, err := canonical.Marshal(build)
		if err != nil {
			return nil, nil, err
		}
		size += len(joined)
		if index != 0 {
			size++ // The comma between adjacent build records.
		}
		if int64(size) > MaxPlanBytes {
			return nil, nil, e.New(e.CodePipelineDrift, "reconstructed Plan exceeds its size bound")
		}
	}
	root["builds"], err = canonical.Marshal(builds)
	if err != nil {
		return nil, nil, err
	}
	raw, err := canonical.Marshal(root)
	if err != nil {
		return nil, nil, err
	}
	if int64(len(raw)) > MaxPlanBytes {
		return nil, nil, e.New(e.CodePipelineDrift, "reconstructed Plan exceeds its size bound")
	}
	var plan Plan
	if err := canonical.UnmarshalExact(raw, &plan); err != nil {
		return nil, nil, e.Wrap(e.CodePipelineDrift, err, "decoding reconstructed Plan")
	}
	if err := plan.Validate(); err != nil {
		return nil, nil, err
	}
	want, err := renderPlanTree(ctx, &plan, planPath)
	if err != nil {
		return nil, nil, err
	}
	for name, expected := range want {
		if !bytes.Equal(files[name], expected) {
			return nil, nil, e.New(e.CodePipelineDrift, "Plan tree member %s differs from reconstructed Plan", name)
		}
	}
	for name := range files {
		if (name == planPath || isPlanStorePath(name) || strings.HasPrefix(name, path.Dir(planPath)+"/steps/")) && want[name] == nil {
			return nil, nil, e.New(e.CodePipelineDrift, "unexpected Plan tree member %q", name)
		}
	}
	return &plan, raw, nil
}

func expandTreeCommands(step planFields, files map[string][]byte, remaining int64) error {
	return treeGraphCommands(step, files, true, remaining)
}

// A path-based load admits only the Plan tree. Complete workflow/action drift
// belongs to CheckGeneratedBundle, not inspection or canonical Plan loading.
func loadPlanTree(ctx context.Context, filename string, rootBytes []byte) (*Plan, string, error) {
	filename, err := filepath.Abs(filename)
	if err != nil {
		return nil, "", e.Wrap(e.CodePipelineDrift, err, "locating Plan tree")
	}
	directory := filepath.Dir(filename)
	root := filepath.Dir(filepath.Dir(directory))
	planPath, err := filepath.Rel(root, filename)
	if err != nil {
		return nil, "", e.Wrap(e.CodePipelineDrift, err, "locating Plan tree")
	}
	planPath = filepath.ToSlash(planPath)
	manifestPath := path.Dir(planPath) + "/manifest.json"
	if err := validateGeneratedDirectoryChain(root, path.Dir(planPath)); err != nil {
		return nil, "", classifyContext(ctx, e.Wrap(e.CodePipelineDrift, err, "Plan tree has unsafe parents"))
	}
	raw, err := readGeneratedContext(ctx, filepath.Join(root, manifestPath))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", e.Wrap(e.CodePipelineDrift, err, "Plan tree root has no bundle manifest; load the committed bundle path or export with run plan --json")
		}
		return nil, "", classifyContext(ctx, e.Wrap(e.CodePipelineDrift, err, "reading Plan tree manifest"))
	}
	var manifest GeneratedManifest
	if err := canonical.UnmarshalExact(bytes.TrimSuffix(raw, []byte{'\n'}), &manifest); err != nil {
		return nil, "", e.Wrap(e.CodePipelineDrift, err, "reading Plan tree manifest")
	}
	encoded, err := canonical.Marshal(manifest)
	if err != nil {
		return nil, "", e.Wrap(e.CodePipelineDrift, err, "encoding Plan tree manifest")
	}
	if manifest.SchemaVersion != generatedManifestSchema || manifest.Engine != EngineGitHub ||
		len(manifest.Members) > maxGeneratedDirEntries || !bytes.Equal(raw, append(encoded, '\n')) {
		return nil, "", e.New(e.CodePipelineDrift, "invalid Plan tree manifest")
	}
	bundle := &GeneratedBundle{Engine: manifest.Engine, WorkflowPath: manifest.WorkflowPath,
		ManifestPath: manifestPath, PlanPath: planPath}
	if err := validateBundleNamespace(bundle); err != nil {
		return nil, "", e.Wrap(e.CodePipelineDrift, err, "invalid Plan tree namespace")
	}
	files := map[string][]byte{}
	bundle.Files = map[string][]byte{}
	size, previous := 0, ""
	for _, member := range manifest.Members {
		// Refuse terminal controls before a path reaches filesystem diagnostics.
		if strings.ContainsFunc(member.Path, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) {
			return nil, "", e.New(e.CodePipelineDrift, "Plan tree manifest path contains control characters")
		}
		if !generatedMemberInNamespace(bundle, member.Path) || member.Mode != 0o644 || member.Path <= previous {
			return nil, "", e.New(e.CodePipelineDrift, "invalid Plan tree manifest member")
		}
		previous = member.Path
		bundle.Files[member.Path] = nil
		if member.Path != planPath && !isPlanStorePath(member.Path) {
			continue
		}
		if err := validateGeneratedDirectoryChain(root, path.Dir(member.Path)); err != nil {
			return nil, "", classifyContext(ctx, e.Wrap(e.CodePipelineDrift, err, "Plan tree member %q has unsafe parents", member.Path))
		}
		name := filepath.Join(root, filepath.FromSlash(member.Path))
		info, err := os.Lstat(name)
		if err != nil || !info.Mode().IsRegular() {
			return nil, "", e.New(e.CodePipelineDrift, "Plan tree member %q is missing or not a regular file", member.Path)
		}
		if info.Mode().Perm() != 0o644 {
			return nil, "", e.New(e.CodePipelineDrift, "Plan tree member %q has mode %04o; expected 0644", member.Path, info.Mode().Perm())
		}
		if info.Size() > int64(maxPlanTreeBytes-size) {
			return nil, "", e.New(e.CodePipelineDrift, "Plan tree member %q exceeds the storage bound", member.Path)
		}
		data, err := readGeneratedContext(ctx, name)
		if err != nil {
			return nil, "", classifyContext(ctx, e.Wrap(e.CodePipelineDrift, err, "reading Plan tree member %q", member.Path))
		}
		size += len(data)
		if size > maxPlanTreeBytes || pipeline.FileDigest(data) != member.SHA256 {
			return nil, "", e.New(e.CodePipelineDrift, "Plan tree member differs from manifest")
		}
		files[member.Path] = data
	}
	storedRoot, listed := files[planPath]
	if !listed {
		return nil, "", e.New(e.CodePipelineDrift, "Plan tree manifest does not list the root")
	}
	if !bytes.Equal(rootBytes, storedRoot) {
		return nil, "", e.New(e.CodePipelineDrift, "Plan tree root changed during admission")
	}
	// The private Plan tree contains only its root index. Reject stale or
	// injected step directories without opening workflow or action contents.
	if _, err := os.Lstat(filepath.Join(directory, "steps")); !os.IsNotExist(err) {
		return nil, "", e.New(e.CodePipelineDrift, "unexpected private Plan steps directory")
	}
	plan, packed, err := joinPlanTree(ctx, planPath, files)
	if err != nil {
		return nil, "", err
	}
	digest, err := plan.Digest()
	if err != nil || digest != manifest.PlanDigest || githubLayoutFor(plan).planPath != planPath {
		return nil, "", e.New(e.CodePipelineDrift, "reconstructed Plan differs from manifest identity or namespace")
	}
	return plan, pipeline.FileDigest(packed), nil
}
