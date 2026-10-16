// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// DiskMaterialRecord is a descriptive export of a live producer-consumption
// trace. Decoding it cannot reconstruct execution, signing or qualification
// authority. Coverage excludes source vendoring and guest/network acquisitions.
type DiskMaterialRecord struct {
	SchemaVersion         string                     `json:"schemaVersion"`
	Lane                  string                     `json:"lane"`
	Output                string                     `json:"output"`
	PlanDigest            string                     `json:"planDigest"`
	BuildDigest           string                     `json:"buildDigest"`
	PayloadDigest         string                     `json:"payloadDigest"`
	Size                  int64                      `json:"size"`
	Format                string                     `json:"format"`
	Encryption            string                     `json:"encryption"`
	Components            []string                   `json:"components"`
	Coverage              string                     `json:"coverage"`
	Members               []DiskMaterialRecordMember `json:"members"`
	Edges                 []DiskMaterialRecordEdge   `json:"edges"`
	UntracedNetworkBuilds []string                   `json:"untracedNetworkBuilds"`
}

type DiskMaterialRecordMember struct {
	Producer   string `json:"producer"`
	Component  string `json:"component"`
	Kind       string `json:"kind"`
	Production string `json:"production"`
	Digest     string `json:"digest"`
	Size       int64  `json:"size"`
}

type DiskMaterialRecordEdge struct {
	Consumer       string `json:"consumer"`
	Binding        string `json:"binding"`
	Producer       string `json:"producer"`
	ManifestDigest string `json:"manifestDigest"`
}

// ObserveDiskMaterialRecord rechecks the original live material handoffs while
// holding the Run's disk lifetime permit. It never reads consumption claims from
// serialized records. A returned record is a defensive description only.
func (owner *LocalRunResult) ObserveDiskMaterialRecord(ctx context.Context, plan *Plan, expected LifecycleOutputPlan) (DiskMaterialRecord, error) {
	if ctx == nil || owner == nil {
		return DiskMaterialRecord{}, e.New(e.CodeUsage, "disk material export requires context and a live local Run")
	}
	release, err := owner.acquireDiskPermit(ctx)
	if err != nil {
		return DiskMaterialRecord{}, err
	}
	defer release()
	trace, err := owner.observeDiskMaterialsWhileHeld(ctx, plan, expected)
	if err != nil {
		return DiskMaterialRecord{}, err
	}
	view := trace.Description()
	record := DiskMaterialRecord{
		SchemaVersion: "nvkr.dev/local-disk-materials/v1", Lane: TierLocalExecution.String(),
		Output: view.OutputName, PlanDigest: view.Subject.PlanDigest, BuildDigest: view.Subject.BuildDigest,
		PayloadDigest: view.Subject.Digest, Size: view.Subject.Size, Format: view.Subject.Format, Encryption: view.Subject.Encryption,
		Components: view.Subject.Components, Coverage: string(view.Coverage),
		Members: []DiskMaterialRecordMember{}, Edges: []DiskMaterialRecordEdge{},
		UntracedNetworkBuilds: append([]string{}, view.UntracedNetworkBuilds...),
	}
	for _, member := range view.Members {
		record.Members = append(record.Members, DiskMaterialRecordMember{Producer: member.Producer, Component: member.Component,
			Kind: member.Kind, Production: string(member.Production), Digest: member.Digest, Size: member.Size})
	}
	for _, edge := range view.Edges {
		record.Edges = append(record.Edges, DiskMaterialRecordEdge(edge))
	}
	if err := record.ValidateDescription(ctx, plan, expected, view.Subject); err != nil {
		return DiskMaterialRecord{}, err
	}
	raw, err := canonical.Marshal(record)
	if err != nil {
		return DiskMaterialRecord{}, err
	}
	if len(raw) > 4<<20 {
		return DiskMaterialRecord{}, e.New(e.CodeProvenanceMissing, "local disk material record exceeds its byte bound")
	}
	if err := ctx.Err(); err != nil {
		return DiskMaterialRecord{}, e.Wrap(e.CodeTimeout, err, "disk material export canceled")
	}
	return record, nil
}
