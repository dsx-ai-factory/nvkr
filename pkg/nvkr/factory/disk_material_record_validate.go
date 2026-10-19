// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"slices"
	"sort"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
)

// ValidateDescription checks a descriptive record against the frozen graph and
// an independently measured disk description. It authenticates neither the
// record nor execution, and never reconstructs a live consumption receipt.
func (record DiskMaterialRecord) ValidateDescription(ctx context.Context, plan *Plan, expected LifecycleOutputPlan, subject DiskPayloadObservation) error {
	if ctx == nil || plan == nil {
		return e.New(e.CodeUsage, "disk material validation requires context and Plan")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "disk material validation canceled")
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	identity, err := plan.IdentityDigest()
	if err != nil {
		return err
	}
	matched := false
	for _, output := range plan.LifecycleOutputs {
		if output.Name == expected.Name {
			matched = output.Kind == PackageDiskImage && expected.Kind == output.Kind && expected.ArtifactID == output.ArtifactID &&
				expected.BuildDigest == output.BuildDigest && slices.Equal(expected.Components, output.Components) && expected.ImageIndex == nil
		}
	}
	if !matched || record.SchemaVersion != "nvkr.dev/local-disk-materials/v1" || record.Lane != TierLocalExecution.String() ||
		record.Output != expected.Name || record.PlanDigest != identity || record.BuildDigest != expected.BuildDigest ||
		!slices.Equal(record.Components, expected.Components) || record.Coverage != string(diskMaterialCoverageStagedOutputs) ||
		subject.PlanDigest != identity || subject.BuildDigest != expected.BuildDigest || !slices.Equal(subject.Components, expected.Components) ||
		record.PayloadDigest != subject.Digest || record.Size != subject.Size || record.Format != subject.Format || record.Encryption != subject.Encryption {
		return e.New(e.CodeVerifyFailed, "disk material record changes its schema, scope or exact disk subject")
	}
	_, artifacts := planIndex(plan)
	raw, ok := artifacts[expected.ArtifactID]
	if !ok || raw.Kind != PackageDiskImage || raw.BuildTarget != expected.Name {
		return e.New(e.CodeVerifyFailed, "disk material record has no exact raw producer")
	}
	build, root, err := plan.producerInstruction(planNode(plan, raw.Producer))
	if err != nil {
		return err
	}
	if build.Name != expected.Name || root.ID != subject.Producer || root.Component != subject.Component || root.Disk == nil ||
		root.Disk.Format != record.Format || root.Disk.Encryption != record.Encryption {
		return e.New(e.CodeVerifyFailed, "disk material record changes its compiled disk producer")
	}
	return record.validateGraphDescription(ctx, build, root.ID)
}

func (record DiskMaterialRecord) validateGraphDescription(ctx context.Context, build BuildPlan, root string) error {
	producers := make(map[string]ir.Node)
	for _, node := range build.Graph.Nodes {
		if node.Kind == ir.KindBuild || node.Kind == ir.KindPull {
			producers[node.ID] = node
		}
	}
	members := make(map[string]DiskMaterialRecordMember)
	previous := ""
	for _, member := range record.Members {
		node, exists := producers[member.Producer]
		production := string(diskMaterialProductionBuilt)
		if node.Kind == ir.KindPull {
			production = string(diskMaterialProductionAcquired)
		}
		if !exists || member.Producer <= previous || member.Component != node.Component || member.Kind != node.OutputType ||
			member.Production != production || member.Size < 0 || !canonical.IsDigest(member.Digest) {
			return e.New(e.CodeVerifyFailed, "disk material record has invalid or substituted producer membership")
		}
		previous = member.Producer
		members[member.Producer] = member
	}
	if member, ok := members[root]; !ok || member.Digest != record.PayloadDigest || member.Size != record.Size {
		return e.New(e.CodeVerifyFailed, "disk material root differs from the measured disk")
	}
	manifests := make(map[string]string)
	for _, edge := range record.Edges {
		if !canonical.IsDigest(edge.ManifestDigest) || (manifests[edge.Consumer] != "" && manifests[edge.Consumer] != edge.ManifestDigest) {
			return e.New(e.CodeVerifyFailed, "disk material consumer has inconsistent manifest identities")
		}
		manifests[edge.Consumer] = edge.ManifestDigest
	}
	var edges []DiskMaterialRecordEdge
	var networks, components []string
	seen := make(map[string]bool)
	var visit func(string) error
	visit = func(id string) error {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "disk material graph validation canceled")
		}
		if seen[id] {
			return nil
		}
		node, exists := producers[id]
		if _, present := members[id]; !exists || !present {
			return e.New(e.CodeVerifyFailed, "disk material record omits a transitive producer")
		}
		seen[id] = true
		components = append(components, node.Component)
		if node.Kind == ir.KindBuild && node.Network != ir.NetworkNone {
			networks = append(networks, id)
		}
		for _, binding := range node.Materials {
			edges = append(edges, DiskMaterialRecordEdge{Consumer: id, Binding: binding.Binding, Producer: binding.Producer, ManifestDigest: manifests[id]})
			if err := visit(binding.Producer); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(root); err != nil {
		return err
	}
	sort.Strings(networks)
	sort.Strings(components)
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].Consumer != edges[j].Consumer {
			return edges[i].Consumer < edges[j].Consumer
		}
		return edges[i].Binding < edges[j].Binding
	})
	if len(seen) != len(members) || !slices.Equal(edges, record.Edges) || !slices.Equal(networks, record.UntracedNetworkBuilds) ||
		!slices.Equal(components, record.Components) {
		return e.New(e.CodeVerifyFailed, "disk material record differs from the complete sorted Plan closure")
	}
	return nil
}
