// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"bytes"
	"encoding/json"
	"path"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// This projection changes storage only. The targets record retains every moved
// byte, including publication-bound derived Build Digests. Joining restores the
// original canonical Plan before any identity or executable admission.
func sliceTreeEnvironment(root planFields) error {
	bindings := planFields{}
	var environment struct {
		Publication *struct {
			RepositoryPrefix json.RawMessage `json:"repositoryPrefix"`
		} `json:"publication"`
	}
	if err := json.Unmarshal(root["environment"], &environment); err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "reading publication namespace")
	}
	if environment.Publication != nil {
		bindings["repositoryPrefix"] = environment.Publication.RepositoryPrefix
	}
	if targets, ok := root["targets"]; ok {
		bindings["targets"] = targets
	}
	if err := treeTargetReferences(bindings, false); err != nil {
		return err
	}
	return mapTreeEnvironment(root, bindings, false)
}

func joinTreeEnvironment(root planFields) error {
	var bindings planFields
	if err := json.Unmarshal(root["targets"], &bindings); err != nil || bindings == nil {
		return e.New(e.CodePipelineDrift, "missing Plan environment bindings")
	}
	if err := treeTargetReferences(bindings, true); err != nil {
		return err
	}
	delete(root, "targets")
	if targets, ok := bindings["targets"]; ok {
		root["targets"] = targets
	}
	return mapTreeEnvironment(root, bindings, true)
}

// Ordinary output repositories repeat one Environment prefix. Store it once;
// nonmatching references retain their original form (for example object routes).
func treeTargetReferences(bindings planFields, expand bool) error {
	if bindings["targets"] == nil || bindings["repositoryPrefix"] == nil {
		return nil
	}
	var prefix string
	if err := json.Unmarshal(bindings["repositoryPrefix"], &prefix); err != nil || prefix == "" {
		return e.New(e.CodePipelineDrift, "invalid stored publication prefix")
	}
	var targets map[string]planFields
	if err := json.Unmarshal(bindings["targets"], &targets); err != nil {
		return e.Wrap(e.CodePipelineDrift, err, "reading stored targets")
	}
	for _, target := range targets {
		if expand {
			if raw, ok := target["repositorySuffix"]; ok {
				var suffix string
				if json.Unmarshal(raw, &suffix) != nil || target["ref"] != nil {
					return e.New(e.CodePipelineDrift, "invalid stored target reference")
				}
				var err error
				target["ref"], err = canonical.Marshal(prefix + "/" + suffix)
				if err != nil {
					return err
				}
				delete(target, "repositorySuffix")
			}
		} else {
			var ref string
			if err := json.Unmarshal(target["ref"], &ref); err != nil {
				return e.Wrap(e.CodePipelineDrift, err, "reading target reference")
			}
			if suffix, ok := strings.CutPrefix(ref, prefix+"/"); ok {
				var err error
				target["repositorySuffix"], err = canonical.Marshal(suffix)
				if err != nil {
					return err
				}
				delete(target, "ref")
			}
		}
	}
	var err error
	bindings["targets"], err = canonical.Marshal(targets)
	return err
}

func mapTreeEnvironment(root, bindings planFields, expand bool) error {
	groups := []struct {
		owner               planFields
		field, key, binding string
	}{{root, "lifecycleOutputs", "name", "outputs"}}
	// Only v2 stores graph nodes and their Environment bindings. V3 derives
	// those nodes directly from the joined Environment and producer records.
	var graph planFields
	if raw, stored := root["graph"]; stored {
		if err := json.Unmarshal(raw, &graph); err != nil {
			return e.Wrap(e.CodePipelineDrift, err, "reading stored graph")
		}
		groups = append(groups, struct {
			owner               planFields
			field, key, binding string
		}{graph, "nodes", "id", "nodes"})
	} else if bindings["nodes"] != nil {
		return e.New(e.CodePipelineDrift, "derived graph cannot have stored node bindings")
	}
	for _, group := range groups {
		if _, exists := group.owner[group.field]; !exists {
			continue
		}
		var records []planFields
		if err := json.Unmarshal(group.owner[group.field], &records); err != nil {
			return e.Wrap(e.CodePipelineDrift, err, "reading environment-bound records")
		}
		values := map[string]planFields{}
		if expand && bindings[group.binding] != nil {
			if err := json.Unmarshal(bindings[group.binding], &values); err != nil {
				return e.Wrap(e.CodePipelineDrift, err, "reading environment bindings")
			}
		}
		for _, record := range records {
			var name string
			if err := json.Unmarshal(record[group.key], &name); err != nil || name == "" {
				return e.New(e.CodePipelineDrift, "environment binding lacks its exact record name")
			}
			if expand {
				if raw, ok := record["derived"]; ok {
					var derived planFields
					if json.Unmarshal(raw, &derived) != nil || derived == nil || derived["repositoryPrefix"] != nil || bindings["repositoryPrefix"] == nil {
						return e.New(e.CodePipelineDrift, "invalid stored derived publication binding")
					}
					derived["repositoryPrefix"] = bindings["repositoryPrefix"]
					var err error
					record["derived"], err = canonical.Marshal(derived)
					if err != nil {
						return err
					}
				}
				if err := mergeTreeBinding(record, values[name]); err != nil {
					return err
				}
				delete(values, name)
				continue
			}
			value := planFields{}
			take := func(field string) {
				if raw, ok := record[field]; ok {
					value[field] = raw
					delete(record, field)
				}
			}
			if group.binding == "nodes" {
				take("runsOn")
				take("executionBinding")
				var kind string
				_ = json.Unmarshal(record["kind"], &kind)
				switch kind {
				case NodeAcquire, NodeVerify, NodeSBOM, NodePackage, NodeInventory:
					take("container")
					take("platform")
				}
			}
			if raw, ok := record["derived"]; ok {
				var derived planFields
				if err := json.Unmarshal(raw, &derived); err != nil {
					return e.Wrap(e.CodePipelineDrift, err, "reading derived binding")
				}
				if prefix, ok := derived["repositoryPrefix"]; ok {
					if !bytes.Equal(prefix, bindings["repositoryPrefix"]) {
						return e.New(e.CodePipelineDrift, "derived output differs from publication namespace")
					}
					var err error
					delete(derived, "repositoryPrefix")
					record["derived"], err = canonical.Marshal(derived)
					if err != nil {
						return err
					}
					if group.binding == "outputs" {
						take("buildDigest")
					}
				}
			}
			if len(value) != 0 {
				values[name] = value
			}
		}
		if expand && len(values) != 0 {
			return e.New(e.CodePipelineDrift, "environment binding names an absent record")
		}
		var err error
		group.owner[group.field], err = canonical.Marshal(records)
		if err != nil {
			return err
		}
		if !expand && len(values) != 0 {
			bindings[group.binding], err = canonical.Marshal(values)
			if err != nil {
				return err
			}
		}
	}
	var err error
	if graph != nil {
		root["graph"], err = canonical.Marshal(graph)
		if err != nil {
			return err
		}
	}
	if !expand {
		root["targets"], err = canonical.Marshal(bindings)
	}
	return err
}

func mergeTreeBinding(record, binding planFields) error {
	for field, value := range binding {
		if _, exists := record[field]; exists {
			return e.New(e.CodePipelineDrift, "Plan field %q has two storage owners", field)
		}
		record[field] = value
	}
	return nil
}

// Catalog and package snapshots and scripts can have multiple variants inside
// one Plan. Other records use stable role names, with hashes in the manifest.
func stablePlanTreeNames(files map[string][]byte, planPath string) (map[string][]byte, error) {
	names := map[string]string{}
	for name := range files {
		if !isPlanStoreRecord(name) || strings.HasPrefix(name, PlanStoreDirectory+"/plan/catalog-") ||
			strings.HasPrefix(name, PlanStoreDirectory+"/plan/package-inputs@") {
			continue
		}
		if at := strings.LastIndex(name, "@"); at >= 0 {
			names[name] = name[:at] + path.Ext(name)
		}
	}
	var root planFields
	if err := decodeTreeJSON(files[planPath], &root); err != nil {
		return nil, err
	}
	rewrite := func(ref json.RawMessage) (json.RawMessage, error) {
		var record map[string]string
		if err := json.Unmarshal(ref, &record); err != nil {
			return nil, e.Wrap(e.CodePipelineDrift, err, "rewriting Plan reference")
		}
		if stable, ok := names[record["file"]]; ok {
			record["file"] = stable
		}
		return canonical.Marshal(record)
	}
	var storage string
	if err := json.Unmarshal(root["storage"], &storage); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "reading Plan storage format")
	}
	for _, field := range planTreeFields(storage) {
		if raw, ok := root[field]; ok {
			value, err := rewrite(raw)
			if err != nil {
				return nil, err
			}
			root[field] = value
		}
	}
	var builds []json.RawMessage
	if err := json.Unmarshal(root["builds"], &builds); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "rewriting build references")
	}
	for i, build := range builds {
		value, err := rewrite(build)
		if err != nil {
			return nil, err
		}
		builds[i] = value
	}
	var err error
	root["builds"], err = canonical.Marshal(builds)
	if err != nil {
		return nil, err
	}
	files[planPath], err = treeJSON(root)
	if err != nil {
		return nil, err
	}
	result := make(map[string][]byte, len(files))
	for _, name := range sortedKeys(files) {
		raw := files[name]
		if stable, ok := names[name]; ok {
			name = stable
		}
		if previous, ok := result[name]; ok && !bytes.Equal(previous, raw) {
			return nil, e.New(e.CodePipelineDrift, "Plan has conflicting stable records at %s", name)
		}
		result[name] = raw
	}
	return result, nil
}
