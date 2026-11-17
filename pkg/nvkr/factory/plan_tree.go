// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"path"
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

func treeName(raw json.RawMessage) (string, error) {
	var name string
	if err := json.Unmarshal(raw, &name); err != nil || !canonicalName.MatchString(name) {
		return "", e.New(e.CodePipelineDrift, "Plan storage requires a safe authored name")
	}
	return name, nil
}

// Aggregate outputs already use output/platform target names. Preserve those
// segments as directories; never flatten or sanitize an identity-bearing name.
func treeTargetName(raw json.RawMessage) (string, error) {
	var name string
	if err := json.Unmarshal(raw, &name); err != nil || !safeGraphID(name) || !safeGeneratedPath(name) {
		return "", e.New(e.CodePipelineDrift, "Plan storage requires a safe target name")
	}
	for _, segment := range strings.Split(name, "/") {
		if !canonicalName.MatchString(segment) {
			return "", e.New(e.CodePipelineDrift, "Plan storage requires safe target segments")
		}
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
	catalogs := planFields{}
	storeCatalog := func(record planFields) error {
		var digest string
		if err := json.Unmarshal(record["catalogDigest"], &digest); err != nil || digest == "" {
			return e.New(e.CodePipelineDrift, "Plan snapshot omits catalogDigest")
		}
		snapshot := record["catalogSnapshot"]
		if previous, found := catalogs[digest]; found && !bytes.Equal(previous, snapshot) {
			return e.New(e.CodePipelineDrift, "Plan catalogDigest names different snapshots")
		}
		catalogs[digest] = snapshot
		delete(record, "catalogSnapshot")
		return nil
	}
	if err := storeCatalog(root); err != nil {
		return nil, err
	}
	files := map[string][]byte{}
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
		record := planFields{"name": build["name"], "catalogDigest": build["catalogDigest"]}
		if resolved, ok := build["resolvedComponents"]; ok {
			record["resolvedComponents"] = resolved
		}
		delete(build, "catalogDigest")
		delete(build, "resolvedComponents")
		directory := path.Join(path.Dir(planPath), "steps", name)
		if err := sliceTreeCommands(build, directory, files); err != nil {
			return nil, err
		}
		stepPath := directory + "/step.json"
		if _, exists := files[stepPath]; exists {
			return nil, e.New(e.CodePipelineDrift, "duplicate Plan target %s", name)
		}
		files[stepPath], err = treeJSON(build)
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
	root["storage"] = json.RawMessage(`"` + planTreeStorage + `"`)
	files[planPath], err = treeJSON(root)
	if err != nil {
		return nil, err
	}
	size := 0
	for name, data := range files {
		size += len(data)
		if int64(len(data)) > maxGeneratedFileBytes || size > maxPlanTreeBytes || len(files) > maxGeneratedDirEntries {
			return nil, e.New(e.CodePipelineDrift, "Plan tree exceeds storage bounds")
		}
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if _, collision := files[parent]; collision {
				return nil, e.New(e.CodePipelineDrift, "Plan tree file/directory collision at %s", parent)
			}
		}
	}
	return files, err
}

// One multiline operand has an unambiguous component-scoped filename. Multiple
// multiline operands retain their exact strings; no ordinal or pin-derived names.
func sliceTreeCommands(build planFields, directory string, files map[string][]byte) error {
	var graph planFields
	if err := json.Unmarshal(build["executionGraph"], &graph); err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "slicing execution graph")
	}
	var nodes []planFields
	if err := json.Unmarshal(graph["nodes"], &nodes); err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "slicing execution nodes")
	}
	for _, node := range nodes {
		for _, field := range []string{"command", "vendorCmd"} {
			if _, ok := node[field]; !ok {
				continue
			}
			var argv []json.RawMessage
			if err := json.Unmarshal(node[field], &argv); err != nil {
				return e.Wrap(e.CodePipelineDrift, err, "slicing command")
			}
			index := -1
			var body string
			for i, arg := range argv {
				var value string
				if err := json.Unmarshal(arg, &value); err != nil {
					return e.Wrap(e.CodePipelineDrift, err, "slicing command operand")
				}
				if strings.Contains(value, "\n") {
					if index >= 0 {
						index = -1
						break
					}
					index, body = i, value
				}
			}
			if index < 0 {
				continue
			}
			component := node["component"]
			prefix := "build-"
			if field == "vendorCmd" {
				prefix = "vendor-"
				component = nil
				for _, consumer := range nodes {
					var needs []json.RawMessage
					if err := json.Unmarshal(consumer["needs"], &needs); err != nil && consumer["needs"] != nil {
						return e.Wrap(e.CodePipelineDrift, err, "reading vendor consumers")
					}
					for _, need := range needs {
						candidate := consumer["component"]
						if bytes.Equal(need, node["id"]) && candidate != nil && (component == nil || bytes.Compare(candidate, component) < 0) {
							component = candidate
						}
					}
				}
			}
			name, err := treeName(component)
			if err != nil {
				return err
			}
			name = prefix + name + ".sh"
			if _, exists := files[directory+"/"+name]; exists {
				return e.New(e.CodePipelineDrift, "Plan script filename collision: %s", name)
			}
			files[directory+"/"+name] = []byte(body)
			argv[index], err = canonical.Marshal(map[string]string{"file": name})
			if err != nil {
				return err
			}
			node[field], err = canonical.Marshal(argv)
			if err != nil {
				return err
			}
		}
	}
	var err error
	graph["nodes"], err = canonical.Marshal(nodes)
	if err != nil {
		return err
	}
	build["executionGraph"], err = canonical.Marshal(graph)
	return err
}
