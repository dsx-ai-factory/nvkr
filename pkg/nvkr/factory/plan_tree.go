// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// This is a storage discriminator, not a Plan schema or executable identity.
const planTreeStorage = "nvkr-plan-tree-v1"

// Raw fields preserve omission, null, integer precision and all existing IR
// variants. Only the existing Plan decoder can admit the joined document.
type planFields map[string]json.RawMessage

func treeJSON(value any) ([]byte, error) {
	raw, err := canonical.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := json.Indent(&out, raw, "", "  "); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "indenting Plan storage")
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

func decodeTreeJSON(raw []byte, value any) error {
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "decoding Plan storage")
	}
	if err := json.Unmarshal(raw, value); err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "decoding Plan storage")
	}
	want, err := treeJSON(value)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, want) {
		return e.New(e.CodePipelineDrift, "Plan storage is not its exact indented representation")
	}
	return nil
}

// Aggregate outputs already use output/platform target names. Preserve those
// segments as directories; never flatten or sanitize an identity-bearing name.
func treeTargetName(raw json.RawMessage) (string, error) {
	var name string
	if err := json.Unmarshal(raw, &name); err != nil || !safeGraphID(name) || !safeGeneratedPath(name) {
		return "", e.New(e.CodePipelineDrift, "Plan storage requires a safe target name")
	}
	return name, nil
}

func renderPlanTree(ctx context.Context, plan *Plan, planPath string) (map[string][]byte, error) {
	raw, err := canonical.Marshal(plan)
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > MaxPlanBytes {
		return nil, e.New(e.CodePipelineDrift, "Plan exceeds its decoded size bound")
	}
	var root planFields
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "slicing Plan")
	}
	var builds []planFields
	if err := json.Unmarshal(root["builds"], &builds); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "slicing Plan builds")
	}
	files := map[string][]byte{}
	catalogs, catalogBytes := planFields{}, planFields{}
	storeCatalog := func(record planFields) error {
		var digest string
		if err := json.Unmarshal(record["catalogDigest"], &digest); err != nil || digest == "" {
			return e.New(e.CodePipelineDrift, "Plan snapshot omits catalogDigest")
		}
		snapshot := record["catalogSnapshot"]
		if previous, found := catalogBytes[digest]; found && !bytes.Equal(previous, snapshot) {
			return e.New(e.CodePipelineDrift, "Plan catalogDigest names different snapshots")
		}
		if _, found := catalogs[digest]; !found {
			ref, err := treeCatalogRecords(snapshot, files, false)
			if err != nil {
				return err
			}
			catalogs[digest], catalogBytes[digest] = ref, snapshot
		}
		delete(record, "catalogSnapshot")
		return nil
	}
	if err := storeCatalog(root); err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for i, build := range builds {
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "slicing Plan canceled")
		}
		name, err := treeTargetName(build["name"])
		if err != nil {
			return nil, err
		}
		if err := storeCatalog(build); err != nil {
			return nil, err
		}
		record := planFields{"name": build["name"]}
		if resolved, ok := build["resolvedComponents"]; ok {
			build["resolvedComponents"], err = treeResolvedRecords(resolved, files, false)
			if err != nil {
				return nil, err
			}
		}
		if names[name] {
			return nil, e.New(e.CodePipelineDrift, "duplicate Plan target %s", name)
		}
		names[name] = true
		if err := sliceTreeCommands(build, files); err != nil {
			return nil, err
		}
		step, err := treeJSON(build)
		if err != nil {
			return nil, err
		}
		platform, err := treeTargetName(build["platform"])
		if err != nil {
			return nil, err
		}
		// The platform is a filename label; the exact full target remains in the
		// record. Aggregate targets already end in that platform.
		logical := strings.TrimSuffix(name, "/"+platform) + "/" + strings.ReplaceAll(platform, "/", "-")
		stepPath, err := namedPlanStorePath("steps/"+logical, step, ".json")
		if err != nil {
			return nil, err
		}
		files[stepPath] = step
		record["file"], err = canonical.Marshal(stepPath)
		if err != nil {
			return nil, err
		}

		builds[i] = record
	}
	root["builds"], err = canonical.Marshal(builds)
	if err != nil {
		return nil, err
	}
	root["catalogSnapshots"], err = canonical.Marshal(catalogs)
	if err != nil {
		return nil, err
	}
	for _, field := range sharedPlanFields {
		if value, ok := root[field]; ok {
			kind := map[string]string{"appliedOverlays": "overlays", "internalTools": "tools", "lifecycleOutputs": "outputs", "runtimeInputs": "runtime-inputs"}[field]
			if kind == "" {
				kind = field
			}
			logical := plan.Definition.Metadata.Name
			if field == "environment" {
				logical = plan.Environment.Metadata.Name
			}
			if field == "actions" {
				logical = "github"
			}
			if logical == "" {
				logical = "plan"
			}
			root[field], err = storeTreeJSON(value, files, "plan/"+kind+"-"+logical)
			if err != nil {
				return nil, err
			}
		}
	}
	root["storage"] = json.RawMessage(`"` + planTreeStorage + `"`)
	files[planPath], err = treeJSON(root)
	if err != nil {
		return nil, err
	}
	size := 0
	for _, data := range files {
		size += len(data)
		if int64(len(data)) > maxGeneratedFileBytes || size > maxPlanTreeBytes || len(files) > maxGeneratedDirEntries {
			return nil, e.New(e.CodePipelineDrift, "Plan tree exceeds storage bounds")
		}
	}
	return files, nil
}

// Every multiline command operand is an immutable exact-byte script object.
func sliceTreeCommands(build planFields, files map[string][]byte) error {
	return treeGraphCommands(build, files, false, MaxPlanBytes)
}
