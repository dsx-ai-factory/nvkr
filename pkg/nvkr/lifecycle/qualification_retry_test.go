// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"regexp"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
)

// These contract fixtures model distinct original signed predicates per subject.
// They exercise the consuming gate with signer callbacks, not a signing service.
func TestQualificationAuthenticatesSameRunAttempts(t *testing.T) {
	const previous = "https://github.com/example/nvkr/actions/runs/42/attempts/1"
	const current = "https://github.com/example/nvkr/actions/runs/42/attempts/2"
	const future = "https://github.com/example/nvkr/actions/runs/42/attempts/3"
	const otherRun = "https://github.com/example/nvkr/actions/runs/43/attempts/1"
	firstOutput := func(request *delivery.PublicationRequest) *delivery.OutputBinding {
		for i := range request.Objects {
			if request.Objects[i].Output != nil {
				return request.Objects[i].Output
			}
		}
		t.Fatal("fixture has no outputs")
		return nil
	}
	previousAggregate := func(request *delivery.PublicationRequest) {
		request.Release.InvocationID = previous
		for i := range request.Objects {
			if origin := request.Objects[i].Output; origin != nil {
				origin.Publication.PreparedInvocationID = previous
				origin.Publication.SignedInvocationID = previous
			}
		}
	}
	for _, tc := range []struct {
		name        string
		publication func(*delivery.PublicationRequest)
		predicate   func(string, *provenance.Predicate)
		code        e.Code
	}{
		{name: "mixed output attempts", publication: func(r *delivery.PublicationRequest) {
			o := firstOutput(r).Publication
			o.PreparedInvocationID, o.SignedInvocationID = previous, previous
		}},
		{name: "output signing retry", publication: func(r *delivery.PublicationRequest) {
			firstOutput(r).Publication.PreparedInvocationID = previous
		}},
		{name: "finalizer reuses aggregate", publication: previousAggregate},
		{name: "aggregate signing retry", publication: previousAggregate, predicate: func(name string, p *provenance.Predicate) {
			if name == "release" {
				p.RunDetails.Metadata["invocationId"] = current
			}
		}},
		{name: "different output run", publication: func(r *delivery.PublicationRequest) {
			o := firstOutput(r).Publication
			o.PreparedInvocationID, o.SignedInvocationID = otherRun, otherRun
		}, code: e.CodeVerifyFailed},
		{name: "future output attempt", publication: func(r *delivery.PublicationRequest) {
			o := firstOutput(r).Publication
			o.PreparedInvocationID, o.SignedInvocationID = future, future
		}, code: e.CodeVerifyFailed},
		{name: "preparation after signing", publication: func(r *delivery.PublicationRequest) {
			firstOutput(r).Publication.PreparedInvocationID = future
		}, code: e.CodeVerifyFailed},
		{name: "output signing after assembly", publication: func(r *delivery.PublicationRequest) {
			r.Release.InvocationID = previous
		}, code: e.CodeVerifyFailed},
		{name: "missing output origin", publication: func(r *delivery.PublicationRequest) {
			firstOutput(r).Publication = nil
		}, code: e.CodeVerifyFailed},
		{name: "different origin repository", publication: func(r *delivery.PublicationRequest) {
			firstOutput(r).Publication.Ref = "registry.example.test/other@sha256:" + strings.Repeat("b", 64)
		}, code: e.CodeVerifyFailed},
		{name: "predicate mismatches original signing", predicate: func(name string, p *provenance.Predicate) {
			if name != "release" {
				p.RunDetails.Metadata["invocationId"] = previous
			}
		}, code: e.CodeVerifyFailed},
		{name: "aggregate signed before assembly", predicate: func(name string, p *provenance.Predicate) {
			if name == "release" {
				p.RunDetails.Metadata["invocationId"] = previous
			}
		}, code: e.CodeVerifyFailed},
		{name: "future aggregate signing", predicate: func(name string, p *provenance.Predicate) {
			if name == "release" {
				p.RunDetails.Metadata["invocationId"] = future
			}
		}, code: e.CodeVerifyFailed},
		{name: "different aggregate run", publication: func(r *delivery.PublicationRequest) {
			r.Release.InvocationID = otherRun
		}, code: e.CodeVerifyFailed},
		{name: "changed predicate inputs", predicate: func(_ string, p *provenance.Predicate) {
			p.BuildDefinition.ExternalParameters["definitionDigest"] = "sha256:" + strings.Repeat("f", 64)
		}, code: e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			identity := "^" + regexp.QuoteMeta(qualificationSourceFixture().value.BuilderID) + "$"
			request, consumer, verifier := newQualificationFixtureWithSigningIdentity(t, identity,
				qualificationFixtureOptions{publication: tc.publication, predicate: tc.predicate})
			receipt, report, err := consumer.checkQualification(t.Context(), request, verifier.source)
			if tc.code != "" {
				assertCode(t, err, tc.code)
				if receipt != nil {
					t.Fatal("invalid publication origins issued qualification authority")
				}
				return
			}
			if err != nil || receipt == nil || report == nil {
				t.Fatalf("valid retry did not issue complete checks: receipt=%v report=%v error=%v", receipt, report, err)
			}
			defer receipt.Close()
			if report.Origin.InvocationID != current || len(report.Outputs) != 2 || len(verifier.verified) != 4 || verifier.evidence != 2 {
				t.Fatalf("retry lost the completion anchor or complete checks: report=%+v signatures=%v evidence=%d", report, verifier.verified, verifier.evidence)
			}
			for _, output := range report.Outputs {
				if output.Overall != "pass" || len(output.Checks) != 6 {
					t.Fatalf("retry skipped required output checks: %+v", output)
				}
				if output.Kind != OutputExecutable {
					continue
				}
				bundle, err := receipt.lifetime.verified.publication.ExecutableBundle(output.Name)
				if err != nil || len(bundle.Entries) == 0 {
					t.Fatalf("retry lost executable bundle: %+v, %v", bundle, err)
				}
				entry := bundle.Entries[0]
				var payload bytes.Buffer
				member, err := receipt.WriteExecutableMember(t.Context(), output.Name, entry.Name, output.Platform, entry.Size, &payload)
				if err != nil || member.Size != entry.Size || member.PayloadDigest != entry.SHA256 || int64(payload.Len()) != entry.Size || pipeline.FileDigest(payload.Bytes()) != entry.SHA256 {
					t.Fatalf("retry could not read exact executable bytes: member=%+v bytes=%d error=%v", member, payload.Len(), err)
				}
			}
			for _, policy := range verifier.policies[1:] {
				if len(policy.ExpectedPredicate) == 0 {
					t.Fatal("retry omitted exact signed predicate verification")
				}
			}
		})
	}
}
