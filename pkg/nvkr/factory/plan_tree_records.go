// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"encoding/json"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// Scalar identity labels and target names stay in the per-bundle index. Frozen
// structured fields, including the complete graph, share exact stored bytes.
var sharedPlanFields = []string{"actions", "appliedOverlays", "composition", "definition", "environment", "graph", "internalTools", "lifecycleOutputs", "routes", "runtimeInputs", "targets"}

func storeTreeJSON(value any, files map[string][]byte) (json.RawMessage, error) {
	raw, err := treeJSON(value)
	if err != nil {
		return nil, err
	}
	name := planStorePath(raw, ".json")
	files[name] = raw
	return canonical.Marshal(map[string]string{"file": name})
}

func readTreeJSON(ref json.RawMessage, files map[string][]byte) (json.RawMessage, error) {
	var record map[string]string
	if err := json.Unmarshal(ref, &record); err != nil || len(record) != 1 || !isPlanStoreRecord(record["file"]) {
		return nil, e.New(e.CodePipelineDrift, "invalid frozen Plan reference")
	}
	raw, ok := files[record["file"]]
	if !ok || planStorePath(raw, ".json") != record["file"] || int64(len(raw)) > maxGeneratedFileBytes {
		return nil, e.New(e.CodePipelineDrift, "missing or altered frozen Plan record")
	}
	var value json.RawMessage
	if err := decodeTreeJSON(raw, &value); err != nil {
		return nil, err
	}
	return canonical.Marshal(value)
}

// Component and source records use the same command extraction as execution
// nodes, so an identical script has one physical copy across all three forms.
func treeRecordCommands(record planFields, files map[string][]byte, expand bool) error {
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
					name := planStorePath([]byte(literal), ".sh")
					files[name] = []byte(literal)
					var err error
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
				if !ok || planStorePath(body, ".sh") != ref["file"] || int64(len(body)) > MaxPlanBytes {
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
			if err := treeRecordCommands(command, files, expand); err != nil {
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
			if expand {
				raw, err = readTreeJSON(raw, files)
				if err != nil {
					return nil, err
				}
			}
			var record planFields
			if err := json.Unmarshal(raw, &record); err != nil {
				return nil, e.Wrap(e.CodePipelineDrift, err, "reading catalog record")
			}
			if err := treeRecordCommands(record, files, expand); err != nil {
				return nil, err
			}
			if expand {
				records[name], err = canonical.Marshal(record)
			} else {
				records[name], err = storeTreeJSON(record, files)
			}
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
	return storeTreeJSON(snapshot, files)
}

// Named resolved components add only a name to the frozen component value. Exact
// matches reuse the snapshot's component object; platform-specific resolutions
// retain their distinct bytes without invoking catalog resolution during load.
func treeResolvedRecords(raw json.RawMessage, files map[string][]byte, expand bool) (json.RawMessage, error) {
	var err error
	if expand {
		raw, err = readTreeJSON(raw, files)
		if err != nil {
			return nil, err
		}
	}
	var records []planFields
	if err := json.Unmarshal(raw, &records); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "reading resolved records")
	}
	size := 0
	for i, record := range records {
		name := record["Name"]
		if expand {
			delete(record, "Name")
			ref, err := canonical.Marshal(record)
			if err != nil {
				return nil, err
			}
			value, err := readTreeJSON(ref, files)
			if err != nil {
				return nil, err
			}
			record = nil
			if err := json.Unmarshal(value, &record); err != nil {
				return nil, e.Wrap(e.CodePipelineDrift, err, "reading resolved component")
			}
			if record["Name"] != nil {
				return nil, e.New(e.CodePipelineDrift, "resolved component has duplicate name")
			}
			if err := treeRecordCommands(record, files, true); err != nil {
				return nil, err
			}
			record["Name"] = name
			records[i] = record
		} else {
			delete(record, "Name")
			if err := treeRecordCommands(record, files, false); err != nil {
				return nil, err
			}
			ref, err := storeTreeJSON(record, files)
			if err != nil {
				return nil, err
			}
			var index planFields
			if err := json.Unmarshal(ref, &index); err != nil {
				return nil, e.Wrap(e.CodePipelineDrift, err, "indexing resolved component")
			}
			index["Name"] = name
			records[i] = index
		}
		value, err := canonical.Marshal(records[i])
		if err != nil {
			return nil, err
		}
		size += len(value)
		if int64(size) > MaxPlanBytes {
			return nil, e.New(e.CodePipelineDrift, "resolved records exceed size bound")
		}
	}
	if expand {
		return canonical.Marshal(records)
	}
	return storeTreeJSON(records, files)
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
		if err := treeRecordCommands(node, files, expand); err != nil {
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
