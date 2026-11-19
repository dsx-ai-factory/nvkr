// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"encoding/json"
	"net/url"
	"path"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Scalar identity labels and target names stay in the per-bundle index. Frozen
// structured fields, including the complete graph, share exact stored bytes.
var sharedPlanFields = []string{"actions", "appliedOverlays", "composition", "definition", "environment", "graph", "internalTools", "lifecycleOutputs", "routes", "runtimeInputs", "targets"}

func storeTreeJSON(value any, files map[string][]byte, logical string) (json.RawMessage, error) {
	raw, err := treeJSON(value)
	if err != nil {
		return nil, err
	}
	name, err := namedPlanStorePath(logical, raw, ".json")
	if err != nil {
		return nil, err
	}
	if prior, exists := files[name]; exists && !bytes.Equal(prior, raw) {
		return nil, e.New(e.CodePipelineDrift, "short Plan address collision at %s", name)
	}
	files[name] = raw
	return canonical.Marshal(map[string]string{"file": name})
}

func readTreeJSON(ref json.RawMessage, files map[string][]byte) (json.RawMessage, error) {
	var record map[string]string
	if err := json.Unmarshal(ref, &record); err != nil || len(record) != 1 || !isPlanStoreRecord(record["file"]) {
		return nil, e.New(e.CodePipelineDrift, "invalid frozen Plan reference")
	}
	raw, ok := files[record["file"]]
	if !ok || !planStoreMatches(record["file"], raw) || int64(len(raw)) > maxGeneratedFileBytes {
		return nil, e.New(e.CodePipelineDrift, "missing or altered frozen Plan record")
	}
	var value json.RawMessage
	if err := decodeTreeJSON(raw, &value); err != nil {
		return nil, err
	}
	return canonical.Marshal(value)
}

// Inline component and source data use the same script extraction as execution
// nodes, so identical command bytes still share their named script object.
func treeRecordCommands(record planFields, files map[string][]byte, expand bool, logical string) error {
	for _, field := range []string{"command", "vendorCmd", "buildCommand", "vendorCommand"} {
		if record[field] == nil {
			continue
		}
		var argv []json.RawMessage
		if err := json.Unmarshal(record[field], &argv); err != nil {
			return e.Wrap(e.CodePipelineDrift, err, "reading frozen command")
		}
		size := 0
		for i, arg := range argv {
			var literal string
			if err := json.Unmarshal(arg, &literal); err == nil && string(arg) != "null" {
				if !expand && strings.Contains(literal, "\n") {
					operation := map[string]string{"command": "build", "buildCommand": "build", "vendorCmd": "vendor", "vendorCommand": "vendor"}[field]
					owner := logical
					if operation == "vendor" {
						var repository string
						_ = json.Unmarshal(record["repo"], &repository)
						if parsed, err := url.Parse(repository); err == nil && parsed.Path != "" {
							owner = strings.TrimSuffix(path.Base(parsed.Path), ".git")
						}
					}
					name, err := namedPlanStorePath("scripts/"+owner+"/"+operation, []byte(literal), ".sh")
					if err != nil {
						return err
					}
					if prior, exists := files[name]; exists && string(prior) != literal {
						return e.New(e.CodePipelineDrift, "short script address collision at %s", name)
					}
					files[name] = []byte(literal)
					argv[i], err = canonical.Marshal(map[string]string{"file": name})
					if err != nil {
						return err
					}
				}
			} else {
				var ref map[string]string
				if !expand || json.Unmarshal(arg, &ref) != nil || len(ref) != 1 || !isPlanStoreScript(ref["file"]) {
					return e.New(e.CodePipelineDrift, "invalid frozen command operand")
				}
				body, ok := files[ref["file"]]
				if !ok || !planStoreMatches(ref["file"], body) || int64(len(body)) > MaxPlanBytes {
					return e.New(e.CodePipelineDrift, "missing, altered or oversized frozen script")
				}
				var err error
				argv[i], err = canonical.Marshal(string(body))
				if err != nil {
					return err
				}
			}
			size += len(argv[i])
			if int64(size) > MaxPlanBytes {
				return e.New(e.CodePipelineDrift, "frozen command exceeds decoded size bound")
			}
		}
		var err error
		record[field], err = canonical.Marshal(argv)
		if err != nil {
			return err
		}
	}
	if raw, ok := record["buildCommandByPlatform"]; ok {
		var commands planFields
		if err := json.Unmarshal(raw, &commands); err != nil {
			return e.Wrap(e.CodePipelineDrift, err, "reading platform commands")
		}
		for name, argv := range commands {
			command := planFields{"buildCommand": argv}
			if err := treeRecordCommands(command, files, expand, logical); err != nil {
				return err
			}
			commands[name] = command["buildCommand"]
		}
		var err error
		record["buildCommandByPlatform"], err = canonical.Marshal(commands)
		if err != nil {
			return err
		}
	}
	return nil
}

func treeCatalogRecords(raw json.RawMessage, files map[string][]byte, expand bool) (json.RawMessage, error) {
	var err error
	if expand {
		raw, err = readTreeJSON(raw, files)
		if err != nil {
			return nil, err
		}
	}
	var snapshot planFields
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "reading frozen catalog")
	}
	for _, field := range []string{"Components", "Sources"} {
		if snapshot[field] == nil {
			continue
		}
		var records planFields
		if err := json.Unmarshal(snapshot[field], &records); err != nil {
			return nil, e.Wrap(e.CodePipelineDrift, err, "reading catalog records")
		}
		size := 0
		for _, name := range sortedKeys(records) {
			raw := records[name]
			var record planFields
			if err := json.Unmarshal(raw, &record); err != nil {
				return nil, e.Wrap(e.CodePipelineDrift, err, "reading catalog record")
			}
			if err := treeRecordCommands(record, files, expand, name); err != nil {
				return nil, err
			}
			records[name], err = canonical.Marshal(record)
			if err != nil {
				return nil, err
			}
			size += len(records[name])
			if int64(size) > MaxPlanBytes {
				return nil, e.New(e.CodePipelineDrift, "expanded catalog records exceed size bound")
			}
		}
		snapshot[field], err = canonical.Marshal(records)
		if err != nil {
			return nil, err
		}
	}
	if expand {
		return canonical.Marshal(snapshot)
	}
	var metadata struct {
		Name string `json:"name"`
	}
	if raw := snapshot["Metadata"]; len(raw) != 0 {
		if err := json.Unmarshal(raw, &metadata); err != nil {
			return nil, e.Wrap(e.CodePipelineDrift, err, "reading frozen catalog name")
		}
	}
	if metadata.Name == "" {
		metadata.Name = "catalog"
	}
	return storeTreeJSON(snapshot, files, "plan/catalog-"+metadata.Name)
}

// Resolved configuration stays beside the execution graph and its source pins.
// Only multiline command operands leave the build record.
func treeResolvedRecords(raw json.RawMessage, files map[string][]byte, expand bool) (json.RawMessage, error) {
	var records []planFields
	if err := json.Unmarshal(raw, &records); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "reading resolved records")
	}
	size := 0
	for _, record := range records {
		var logical string
		if err := json.Unmarshal(record["Name"], &logical); err != nil {
			return nil, e.Wrap(e.CodePipelineDrift, err, "reading resolved component name")
		}
		if err := treeRecordCommands(record, files, expand, logical); err != nil {
			return nil, err
		}
		value, err := canonical.Marshal(record)
		if err != nil {
			return nil, err
		}
		size += len(value)
		if int64(size) > MaxPlanBytes {
			return nil, e.New(e.CodePipelineDrift, "resolved records exceed size bound")
		}
	}
	return canonical.Marshal(records)
}

func treeGraphCommands(step planFields, files map[string][]byte, expand bool, remaining int64) error {
	var graph planFields
	if err := json.Unmarshal(step["executionGraph"], &graph); err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "reading frozen graph")
	}
	var nodes []planFields
	if err := json.Unmarshal(graph["nodes"], &nodes); err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "reading frozen graph nodes")
	}
	size := int64(0)
	for _, node := range nodes {
		var logical string
		_ = json.Unmarshal(node["component"], &logical)
		if logical == "" {
			logical = "execution"
		}
		if err := treeRecordCommands(node, files, expand, logical); err != nil {
			return err
		}
		raw, err := canonical.Marshal(node)
		if err != nil {
			return err
		}
		size += int64(len(raw))
		if size > remaining {
			return e.New(e.CodePipelineDrift, "Plan commands exceed decoded size bound")
		}
	}
	var err error
	graph["nodes"], err = canonical.Marshal(nodes)
	if err != nil {
		return err
	}
	step["executionGraph"], err = canonical.Marshal(graph)
	return err
}
