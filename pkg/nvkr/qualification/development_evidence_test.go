// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualification

import (
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/development"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualificationorigin"
)

func developmentEvidenceFixture() Evidence {
	v := evidenceFixture()
	v.SchemaVersion, v.GateVersion = DevelopmentSchemaVersion, DevelopmentGateVersion
	update := func(d *qualificationorigin.Description, workflow, branch string) {
		d.Workflow, d.Branch, d.Ref = workflow, branch, "refs/heads/"+branch
		d.BuilderID = "https://github.com/" + d.Repository + "/" + workflow + "@" + d.Ref
	}
	update(&v.ArtifactChecks.Origin, development.ProducerWorkflow, "feature/build")
	update(&v.Invocation, development.QualificationWorkflow, "feature/qualify")
	prefix := "ghcr.io/example/nvkr/development/runs/12/attempts/1/"
	v.ArtifactChecks.Inputs.ReleaseSubject = prefix + "releases@" + v.ArtifactChecks.Inputs.ContentSubject
	for i := range v.ArtifactChecks.Outputs {
		o := &v.ArtifactChecks.Outputs[i]
		o.Reference = prefix + o.Name + "@" + o.Subject
	}
	p := &v.ToolSmoke.Policy
	p.Source.Development, p.Source.Workflow = true, development.ProducerWorkflow
	p.QualificationWorkflow, p.Smoke.Workflow = development.QualificationWorkflow, development.SmokeWorkflow
	digest, err := p.Digest()
	if err != nil {
		panic(err)
	}
	v.ToolSmoke.PolicyDigest = digest
	v.ToolSmoke.ArtifactChecks, v.ToolSmoke.Invocation = v.ArtifactChecks, v.Invocation
	for i := range v.ToolSmoke.Workers {
		w := &v.ToolSmoke.Workers[i]
		w.ArtifactChecks, w.Invocation, w.PolicyDigest = v.ArtifactChecks, v.Invocation, digest
	}
	return v
}

func TestDevelopmentEvidenceIsNotProductionEvidence(t *testing.T) {
	v := developmentEvidenceFixture()
	raw, err := v.Canonical()
	if err != nil {
		t.Fatal(err)
	}
	_, err = Decode(raw)
	requireCode(t, err, e.CodeVerifyFailed)
	for _, tc := range []struct {
		name string
		edit func(*Evidence)
	}{
		{"source attempt", func(v *Evidence) {
			v.ArtifactChecks.Inputs.ReleaseSubject = strings.Replace(v.ArtifactChecks.Inputs.ReleaseSubject, "/attempts/1/", "/attempts/2/", 1)
		}},
		{"output attempt", func(v *Evidence) {
			v.ArtifactChecks.Outputs[0].Reference = strings.Replace(v.ArtifactChecks.Outputs[0].Reference, "/runs/12/", "/runs/13/", 1)
		}},
		{"production schema", func(v *Evidence) { v.SchemaVersion, v.GateVersion = SchemaVersion, GateVersion }},
		{"production source", func(v *Evidence) { v.ArtifactChecks.Origin.Branch = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := developmentEvidenceFixture()
			tc.edit(&v)
			requireCode(t, v.Validate(), e.CodeVerifyFailed)
		})
	}
}
