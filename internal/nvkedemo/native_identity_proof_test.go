// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/catalog"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/siteidentity"
)

func nativeIdentityProofFixture(t *testing.T) (nativeStaticEntry, nativeStaticEntrySpec, nativeIdentityObservation, nativeIdentityObservation, siteidentity.Result, time.Time, string) {
	t.Helper()
	tpm := strings.Repeat("a", 64)
	raw := []byte(`{"kind":"ClusterStaticEntry","metadata":{"name":"host-fixture","labels":{"nvke.nvidia.com/managed-by":"machine-spire-entry-controller"}},"spec":{"className":"fixture-class","parentID":"spiffe://site.example.test/spire/agent/tpm/` + tpm + `","spiffeID":"spiffe://site.example.test/host/fixture","selectors":["unix:uid:0"],"x509SVIDTTL":"0s"}}`)
	source, spec, err := nativeSourceStaticEntry(raw, tpm)
	if err != nil {
		t.Fatal(err)
	}
	actual := source
	actual.Metadata.UID = "cse-fixture"
	yes, no := true, false
	actual.Status.Rendered = &yes
	actual.Status.Masked = &no
	actual.Status.Set = &no
	registration := nativeSpireRegistration{ID: "registration-fixture", SPIFFEID: nativeSpireID{"site.example.test", "/host/fixture"}, ParentID: nativeSpireID{"site.example.test", "/spire/agent/tpm/" + tpm}}
	registration.Selectors = append(registration.Selectors, struct{ Type, Value string }{"unix", "uid:0"})
	now := time.Unix(1800000000, 0).UTC()
	agent := nativeSpireAgent{ID: registration.ParentID, AttestationType: "tpm", Banned: &no, ExpiresAt: json.Number("1800003600"), Serial: json.Number("123")}
	after := nativeIdentityObservation{entries: []nativeStaticEntry{actual}, allowlist: map[string]string{tpm: "nvke.nvidia.com/managed-by=machine-spire-entry-controller"}, agents: nativeSpireAgents{Agents: []nativeSpireAgent{agent}}, registrations: nativeSpireRegistrations{Entries: []nativeSpireRegistration{registration}}}
	result := siteidentity.Result{SchemaVersion: siteidentity.SchemaVersion, Completion: siteidentity.JoinedSuccess, Operation: "handoff", Before: siteidentity.PeerProof{PeerID: spec.SPIFFEID, Serial: "abc", BootID: "fixture-boot"}, Script: &siteidentity.ScriptResult{Outcome: "applied", NodeAgentPID: 100}, After: &siteidentity.PeerProof{PeerID: spec.SPIFFEID, Serial: "def", BootID: "fixture-boot", NotBefore: now.Add(-10 * time.Second), NotAfter: now.Add(time.Hour)}}
	return source, spec, nativeIdentityObservation{}, after, result, now, tpm
}
func TestNativeIdentitySPIREAgentJSONUsesSourceFieldNames(t *testing.T) {
	for _, tc := range []struct{ name, serial, expiry string }{
		{"source CLI quoted integers", `"123456789012345678901234567890"`, `"1800003600"`},
		{"numeric fields", `123456789012345678901234567890`, `1800003600`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rows nativeSpireAgents
			raw := []byte(`{"agents":[{"id":{"trust_domain":"site.example.test","path":"/spire/agent/tpm/fixture"},"attestation_type":"tpm","banned":false,"x509svid_serial_number":` + tc.serial + `,"x509svid_expires_at":` + tc.expiry + `}],"next_page_token":""}`)
			if err := nativeIdentityJSON(raw, &rows); err != nil {
				t.Fatal(err)
			}
			if len(rows.Agents) != 1 || rows.Agents[0].Serial.String() != "123456789012345678901234567890" || rows.Agents[0].ExpiresAt.String() != "1800003600" || rows.Agents[0].Banned == nil || *rows.Agents[0].Banned {
				t.Fatal("source CLI integer or banned fields were lost")
			}
		})
	}
}
func TestNativeIdentitySourceAndPermanentPolicy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(*nativeIdentityObservation)
		wantError bool
	}{
		{name: "steady status set false is valid"},
		{"created CSE was replaced", func(v *nativeIdentityObservation) { v.entries[0].Metadata.UID = "replacement-fixture" }, true},
		{"unrendered", func(v *nativeIdentityObservation) { no := false; v.entries[0].Status.Rendered = &no }, true},
		{"masked", func(v *nativeIdentityObservation) { yes := true; v.entries[0].Status.Masked = &yes }, true},
		{"different source spec", func(v *nativeIdentityObservation) { v.entries[0].Spec = []byte(`{"className":"other"}`) }, true},
		{"missing source label", func(v *nativeIdentityObservation) { v.entries[0].Metadata.Labels = nil }, true},
		{"derived allowlist absent", func(v *nativeIdentityObservation) { v.allowlist = nil }, true},
		{"registration selectors differ", func(v *nativeIdentityObservation) { v.registrations.Entries[0].Selectors = nil }, true},
		{"administrative registration", func(v *nativeIdentityObservation) { v.registrations.Entries[0].Admin = true }, true},
		{"duplicate registration", func(v *nativeIdentityObservation) {
			v.registrations.Entries = append(v.registrations.Entries, v.registrations.Entries[0])
		}, true},
		{"incomplete registration page", func(v *nativeIdentityObservation) { v.registrations.NextPageToken = "another" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, spec, _, after, _, _, tpm := nativeIdentityProofFixture(t)
			if tc.mutate != nil {
				tc.mutate(&after)
			}
			_, _, err := nativeIdentityPolicy(source, spec, tpm, "cse-fixture", after)
			if (err != nil) != tc.wantError {
				t.Fatalf("policy error=%v wantError=%v", err, tc.wantError)
			}
		})
	}
}
func TestNativeIdentityBaselineAndFreshTPMAreRequired(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(*nativeIdentityObservation, *nativeIdentityObservation, *siteidentity.Result)
		wantError bool
	}{
		{name: "new TPM and fresh exact host"},
		{"TPM already existed", func(b, a *nativeIdentityObservation, _ *siteidentity.Result) { b.agents = a.agents }, true},
		{"not TPM", func(_, a *nativeIdentityObservation, _ *siteidentity.Result) {
			a.agents.Agents[0].AttestationType = "join_token"
		}, true},
		{"banned", func(_, a *nativeIdentityObservation, _ *siteidentity.Result) {
			yes := true
			a.agents.Agents[0].Banned = &yes
		}, true},
		{"expired agent", func(_, a *nativeIdentityObservation, _ *siteidentity.Result) { a.agents.Agents[0].ExpiresAt = "1" }, true},
		{"missing serial", func(_, a *nativeIdentityObservation, _ *siteidentity.Result) { a.agents.Agents[0].Serial = "0" }, true},
		{"missing fresh peer", func(_, _ *nativeIdentityObservation, r *siteidentity.Result) { r.After = nil }, true},
		{"same cached serial", func(_, _ *nativeIdentityObservation, r *siteidentity.Result) { r.After.Serial = r.Before.Serial }, true},
		{"changed boot", func(_, _ *nativeIdentityObservation, r *siteidentity.Result) { r.After.BootID = "different" }, true},
		{"changed peer", func(_, _ *nativeIdentityObservation, r *siteidentity.Result) {
			r.After.PeerID = "spiffe://site.example.test/host/other"
		}, true},
		{"old valid certificate", func(_, _ *nativeIdentityObservation, r *siteidentity.Result) {
			r.After.NotBefore = r.After.NotBefore.Add(-time.Hour)
		}, true},
		{"future certificate", func(_, _ *nativeIdentityObservation, r *siteidentity.Result) {
			r.After.NotBefore = r.After.NotBefore.Add(time.Hour)
		}, true},
		{"remote completion unknown", func(_, _ *nativeIdentityObservation, r *siteidentity.Result) { r.Completion = siteidentity.Unknown }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, spec, before, after, result, now, _ := nativeIdentityProofFixture(t)
			if tc.mutate != nil {
				tc.mutate(&before, &after, &result)
			}
			_, err := nativeIdentityFreshTPM(spec, before, after, result, now, now, "fixture-boot")
			if (err != nil) != tc.wantError {
				t.Fatalf("proof error=%v wantError=%v", err, tc.wantError)
			}
		})
	}
	source, spec, before, after, _, _, _ := nativeIdentityProofFixture(t)
	if err := nativeIdentityBaseline(source, spec, before); err != nil {
		t.Fatal(err)
	}
	before.entries = after.entries
	if err := nativeIdentityBaseline(source, spec, before); err == nil {
		t.Fatal("existing source policy admitted as a first import")
	}
}
func TestNativeIdentityCompletionControlsDependencyLifetime(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		completion             siteidentity.Completion
		commandErr             error
		raw                    string
		cancel                 bool
		wantUnknown, wantError bool
	}{
		{name: "joined success", completion: siteidentity.JoinedSuccess},
		{name: "joined helper failure", completion: siteidentity.JoinedFailure, commandErr: &CommandError{ExitCode: 17}, wantError: true},
		{name: "unknown exit zero", completion: siteidentity.Unknown, wantUnknown: true, wantError: true},
		{name: "missing result", raw: "missing", wantUnknown: true, wantError: true},
		{name: "unjoined local child", completion: siteidentity.JoinedSuccess, commandErr: ErrUnjoined, wantUnknown: true, wantError: true},
		{name: "parent cancellation drains accepted call", completion: siteidentity.JoinedSuccess, cancel: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			attempt := &nativeSiteAttempt{}
			_, err := attempt.captureNativeIdentityMutation(ctx, time.Second, func(critical context.Context) ([]byte, error) {
				if tc.cancel {
					cancel()
					if critical.Err() != nil {
						t.Fatal("parent cancellation interrupted remote completion drain")
					}
				}
				if tc.raw != "" {
					return []byte(tc.raw), tc.commandErr
				}
				raw, encodeErr := canonical.Marshal(siteidentity.Result{SchemaVersion: siteidentity.SchemaVersion, Completion: tc.completion})
				if encodeErr != nil {
					t.Fatal(encodeErr)
				}
				return append(raw, '\n'), tc.commandErr
			})
			if (err != nil) != tc.wantError || errors.Is(err, ErrUnjoined) != tc.wantUnknown || errors.Is(attempt.unjoinedWork, ErrUnjoined) != tc.wantUnknown {
				t.Fatalf("err=%v retained=%v", err, attempt.unjoinedWork)
			}
		})
	}
	for _, goexit := range []bool{false, true} {
		t.Run(fmt.Sprintf("unwind-goexit-%v", goexit), func(t *testing.T) {
			attempt := &nativeSiteAttempt{}
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer func() { _ = recover() }()
				_, _ = attempt.captureNativeIdentityMutation(t.Context(), time.Second, func(context.Context) ([]byte, error) {
					if goexit {
						runtime.Goexit()
					}
					panic("fixture")
				})
			}()
			<-done
			if !errors.Is(attempt.unjoinedWork, ErrUnjoined) {
				t.Fatal("unwind lost dependency retention")
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	called := false
	attempt := &nativeSiteAttempt{}
	_, err := attempt.captureNativeIdentityMutation(ctx, time.Second, func(context.Context) ([]byte, error) { called = true; return nil, nil })
	if called || !errors.Is(err, context.Canceled) || attempt.unjoinedWork != nil {
		t.Fatalf("pre-dispatch cancellation called=%v err=%v retained=%v", called, err, attempt.unjoinedWork)
	}
}
func TestNativeIdentityRendererSourceFollowsSelectedOSMaterial(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(*factory.Plan, *factory.Plan)
		wantError bool
	}{
		{name: "SPIRE exception independent of ordinary tools"},
		{"different renderer commit", func(a, _ *factory.Plan) { a.Builds[0].ResolvedComponents[0].Commit = "other" }, true},
		{"different renderer tree", func(a, _ *factory.Plan) { a.Builds[0].ResolvedComponents[0].TreeHash = "other" }, true},
		{"missing helper", func(a, _ *factory.Plan) { a.Builds[0].ResolvedComponents = a.Builds[0].ResolvedComponents[:1] }, true},
		{"wrong architecture", func(a, _ *factory.Plan) { a.Builds[0].Platform = "darwin/arm64" }, true},
		{"wrong output build", func(_, a *factory.Plan) { a.Builds[0].BuildDigest = "other" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spire := catalog.Component{Repo: "https://github.com/example/public", Commit: "spire-package-source", TreeHash: "spire-source-tree", FromSource: true}
			helper := catalog.Component{Repo: "https://github.com/example/nvkr", Commit: "helper-source", TreeHash: "helper-tree", FromSource: true}
			tools := &factory.Plan{Builds: []factory.BuildPlan{{Name: "site-startup-tools-arm64", BuildDigest: "tools-build", Platform: "linux/arm64", ResolvedComponents: []catalog.NamedComponent{{Name: nativeSpireRendererSource, Component: spire}, {Name: nativeIdentityHelper, Component: helper}}}}}
			disk := &factory.Plan{Builds: []factory.BuildPlan{{Name: DiskOutputName, BuildDigest: "disk-build", Platform: "linux/arm64", ResolvedComponents: []catalog.NamedComponent{{Name: "nvke-spire-agent", Component: spire}}}}}
			if tc.mutate != nil {
				tc.mutate(tools, disk)
			}
			err := nativeIdentitySources(tools, disk, "tools-build", "disk-build", DiskOutputName)
			if (err != nil) != tc.wantError {
				t.Fatalf("source error=%v wantError=%v", err, tc.wantError)
			}
		})
	}
}

func TestNativeIdentityCreatedEntryBindsExactSourceAndServerUID(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(*nativeStaticEntry)
		wantError bool
	}{
		{name: "exact server-created source"},
		{"no server UID", func(v *nativeStaticEntry) { v.Metadata.UID = "" }, true},
		{"wrong name", func(v *nativeStaticEntry) { v.Metadata.Name = "other-fixture" }, true},
		{"changed source spec", func(v *nativeStaticEntry) { v.Spec = []byte(`{"className":"changed-fixture"}`) }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, _, _, after, _, _, tpm := nativeIdentityProofFixture(t)
			created := after.entries[0]
			if tc.mutate != nil {
				tc.mutate(&created)
			}
			raw, err := json.Marshal(created)
			if err != nil {
				t.Fatal(err)
			}
			uid, err := nativeIdentityCreatedEntry(raw, source, tpm)
			if (err != nil) != tc.wantError || !tc.wantError && uid != "cse-fixture" {
				t.Fatalf("created UID=%q error=%v wantError=%v", uid, err, tc.wantError)
			}
		})
	}
}
