// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

type planStoreIndexDocument struct {
	Objects map[string][]planStoreVariant    `json:"objects"`
	Jobs    map[string][]planStoreJobVariant `json:"jobs"`
}

type planStoreJobVariant struct {
	Operations []githubScriptOperation `json:"operations"`
	Workflows  []planStoreWorkflow     `json:"workflows"`
}

type planStoreWorkflow struct {
	Bundle string `json:"bundle"`
	File   string `json:"file"`
}

// Reconstruct navigation from each published bundle's manifest-owned records.
// This is a generation/drift operation; ordinary Plan loads never consult it.
func planStoreNavigation(ctx context.Context, root string, owners map[string]planStoreOwner, incoming *GeneratedBundle) (map[string][]planStoreJobVariant, error) {
	bundles := map[string]bool{}
	for _, owner := range owners {
		for _, bundle := range owner.Bundles {
			bundles[bundle] = true
		}
	}
	jobs := map[string][]planStoreJobVariant{}
	for _, name := range sortedKeys(bundles) {
		layout, ok := githubWorkflowNamespace(".github/workflows/" + name + ".yml")
		if !ok {
			return nil, e.New(e.CodePipelineDrift, "invalid navigation bundle")
		}
		bundle := &GeneratedBundle{Engine: EngineGitHub, WorkflowPath: layout.workflowPath, PlanPath: layout.planPath, ManifestPath: layout.manifestPath}
		var files map[string][]byte
		var err error
		if incoming != nil && incoming.ManifestPath == bundle.ManifestPath {
			files = make(map[string][]byte, len(incoming.Files))
			for name, raw := range incoming.Files {
				files[name] = raw
			}
		} else {
			files, err = readPlanStoreNavigation(ctx, root, bundle)
			if err != nil {
				return nil, err
			}
		}
		var navigation map[string]githubScriptJob
		legacy := filepath.ToSlash(filepath.Join(filepath.Dir(layout.planPath), "script-index.json"))
		if raw, ok := files[legacy]; ok {
			// Existing manifests retain retirement authority while bundles migrate one
			// at a time. This descriptive source disappears with its owning manifest.
			navigation, err = legacyPlanStoreNavigation(raw)
		} else {
			var plan *Plan
			plan, _, err = joinPlanTree(ctx, layout.planPath, files)
			if err == nil {
				navigation, err = githubScriptNavigation(plan, files)
			}
		}
		if err != nil {
			return nil, err
		}
		for _, jobID := range sortedKeys(navigation) {
			job := navigation[jobID]
			variants := jobs[jobID]
			matched := -1
			for i, variant := range variants {
				if reflect.DeepEqual(variant.Operations, job.Operations) {
					matched = i
					break
				}
			}
			if matched < 0 {
				variants = append(variants, planStoreJobVariant{Operations: job.Operations})
				matched = len(variants) - 1
			}
			variants[matched].Workflows = append(variants[matched].Workflows, planStoreWorkflow{Bundle: name, File: job.Workflow})
			jobs[jobID] = variants
		}
	}
	return jobs, nil
}

func readPlanStoreNavigation(ctx context.Context, root string, bundle *GeneratedBundle) (map[string][]byte, error) {
	members, err := priorGeneratedMembers(ctx, root, bundle)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	size := int64(0)
	legacy := filepath.ToSlash(filepath.Join(filepath.Dir(bundle.PlanPath), "script-index.json"))
	for _, member := range members {
		if member.Path != bundle.PlanPath && member.Path != legacy && !isPlanStorePath(member.Path) && !(strings.HasPrefix(member.Path, ".github/workflows/") && strings.HasSuffix(member.Path, ".yml")) {
			continue
		}
		if err := validateGeneratedDirectoryChain(root, filepath.ToSlash(filepath.Dir(member.Path))); err != nil {
			return nil, err
		}
		filename := filepath.Join(root, member.Path)
		info, err := os.Lstat(filename)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 || info.Size() > maxPlanTreeBytes-size {
			return nil, e.New(e.CodePipelineDrift, "unsafe or oversized navigation member %q", member.Path)
		}
		raw, err := readGeneratedContext(ctx, filename)
		if err != nil {
			return nil, err
		}
		size += int64(len(raw))
		if size > maxPlanTreeBytes || pipeline.FileDigest(raw) != member.SHA256 {
			return nil, e.New(e.CodePipelineDrift, "navigation member differs from manifest")
		}
		files[member.Path] = raw
	}
	return files, nil
}

func legacyPlanStoreNavigation(raw []byte) (map[string]githubScriptJob, error) {
	var old struct {
		Jobs map[string]struct {
			Workflow   string `json:"workflow"`
			Operations []struct {
				Node          string          `json:"node"`
				Step          string          `json:"step"`
				Target        string          `json:"target"`
				Producer      string          `json:"producer"`
				Record        string          `json:"record"`
				Command       json.RawMessage `json:"command"`
				VendorCommand json.RawMessage `json:"vendorCmd"`
			} `json:"operations"`
		} `json:"jobs"`
	}
	if err := json.Unmarshal(raw, &old); err != nil {
		return nil, e.Wrap(e.CodePipelineDrift, err, "reading retiring script index")
	}
	jobs := map[string]githubScriptJob{}
	for id, job := range old.Jobs {
		next := githubScriptJob{Workflow: job.Workflow, Operations: []githubScriptOperation{}}
		for _, operation := range job.Operations {
			scripts, err := githubCommandScripts(operation.Command, operation.VendorCommand)
			if err != nil {
				return nil, err
			}
			next.Operations = append(next.Operations, githubScriptOperation{Node: operation.Node, Step: operation.Step, Target: operation.Target, Producer: operation.Producer, Record: operation.Record, Scripts: scripts})
		}
		jobs[id] = next
	}
	return jobs, nil
}
