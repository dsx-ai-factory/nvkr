// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"sort"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// QualificationControl describes an original generated metadata carrier. It is
// a selection, never artifact or source authority.
type QualificationControl struct{ ID, Name, Member, Root string }

func AdmitDevelopmentQualification(plan *Plan) error {
	if plan == nil {
		return e.New(e.CodeUsage, "development qualification requires an original Plan")
	}
	if len(plan.SourceOverrides) != 0 {
		return e.New(e.CodeEngineCapability, "development qualification rejects source overrides; such Runs are diagnostics")
	}
	if !plan.Environment.GitHubDevelopment || plan.Environment.Signing == nil || plan.Environment.Signing.Mode != SigningModeCosign || !PerOutputPublication(plan) {
		return e.New(e.CodeEngineCapability, "development qualification requires signed per-output development publication")
	}
	return plan.Validate()
}

// DevelopmentQualificationControls selects only original public control records;
// payload access and cryptographic verification remain with the consumer.
func DevelopmentQualificationControls(plan *Plan) ([]QualificationControl, error) {
	if err := AdmitDevelopmentQualification(plan); err != nil {
		return nil, err
	}
	_, artifacts := planIndex(plan)
	ids := []string{"publication/release", "metadata/release", "predicate/release"}
	for _, output := range plan.LifecycleOutputs {
		ids = append(ids, "predicate/"+outputPublicationRoute(output.Name))
	}
	sort.Strings(ids)
	result := make([]QualificationControl, 0, len(ids))
	for _, id := range ids {
		artifact, ok := artifacts[id]
		if !ok {
			return nil, e.New(e.CodeProvenanceMissing, "development Plan omits %s", id)
		}
		result = append(result, QualificationControl{ID: id, Name: githubArtifactName(id), Member: carrierPath(id), Root: artifact.Path})
	}
	return result, nil
}
