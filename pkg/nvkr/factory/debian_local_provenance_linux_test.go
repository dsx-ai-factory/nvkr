// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package factory

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/provenance"
)

func TestDebianLocalProvenanceNativeOriginalProducer(t *testing.T) {
	if os.Getenv("NVKR_TEST_DEBIAN_CONTENT_RUNTIME") != "1" {
		t.Skip("explicit native provenance contract requires real composition capacity")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	plan, result, inspector, _ := debianRunContentFixture(t, ctx, "issued")
	output := plan.LifecycleOutputs[0]
	if _, err := result.debian.inspectBoundProducer(ctx, result, plan, output, inspector); err != nil {
		t.Fatal(err)
	}
	receipt, err := result.debian.observeBoundOutput(ctx, result, plan, output)
	if err != nil {
		t.Fatal(err)
	}
	raw, observation, err := receipt.localProducerProvenance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var predicate provenance.Predicate
	if err := canonical.UnmarshalExact(raw, &predicate); err != nil {
		t.Fatal(err)
	}
	if err := provenance.ValidateProduction(predicate); err != nil {
		t.Fatal(err)
	}
	completion := result.debian.completions[output.ArtifactID]
	started, err := time.Parse(time.RFC3339Nano, predicate.RunDetails.Metadata["startedOn"])
	if err != nil || !started.Equal(completion.startedOn) {
		t.Fatal("provenance replaced original start", err)
	}
	finished, err := time.Parse(time.RFC3339Nano, predicate.RunDetails.Metadata["finishedOn"])
	if err != nil || !finished.Equal(completion.finishedOn) {
		t.Fatal("provenance replaced original completion", err)
	}
	if predicate.RunDetails.Builder.ID != provenance.LocalBuilderID || predicate.BuildDefinition.InternalParameters["signingMode"] != provenance.SigningModeDryRun || predicate.BuildDefinition.ExternalParameters["planDigest"] != observation.PlanDigest {
		t.Fatal("local provenance invented hosted or signed identity")
	}
	prepared := false
	for _, dependency := range predicate.BuildDefinition.ResolvedDependencies {
		if dependency.URI == "nvkr:prepared-source" {
			prepared = dependency.Digest["sha256"] == strings.TrimPrefix(observation.InputDigest, "sha256:")
		}
	}
	if !prepared {
		t.Fatal("provenance omitted original prepared source")
	}
	result.Workspace = "caller description mutation"
	t.Setenv("GITHUB_ACTIONS", "true")
	readback, observed, err := receipt.localProducerProvenance(ctx)
	if err != nil || string(readback) != string(raw) || observation != observed {
		t.Fatal("caller Run description changed producer evidence", err)
	}
	result.debian.mu.Lock()
	changed := completion
	changed.startedOn = time.Time{}
	result.debian.completions[output.ArtifactID] = changed
	result.debian.mu.Unlock()
	missing, zero, err := receipt.localProducerProvenance(ctx)
	if missing != nil || zero != (DebianOutputObservation{}) || factoryErrorCode(err) != e.CodeProvenanceMissing {
		t.Fatal("missing original time supplied evidence", err)
	}
	result.debian.mu.Lock()
	result.debian.completions[output.ArtifactID] = completion
	result.debian.mu.Unlock()
	result.debian.mu.Lock()
	changed = completion
	changed.startedOn = completion.finishedOn.Add(time.Second)
	result.debian.completions[output.ArtifactID] = changed
	result.debian.mu.Unlock()
	if raw, zero, err := receipt.localProducerProvenance(ctx); raw != nil || zero != (DebianOutputObservation{}) || factoryErrorCode(err) != e.CodeUnverifiedRef {
		t.Fatal("reversed original time supplied evidence", err)
	}
	result.debian.mu.Lock()
	result.debian.completions[output.ArtifactID] = completion
	result.debian.mu.Unlock()
	canceled, stop := context.WithCancel(ctx)
	stop()
	if raw, zero, err := receipt.localProducerProvenance(canceled); raw != nil || zero != (DebianOutputObservation{}) || factoryErrorCode(err) != e.CodeTimeout {
		t.Fatal("canceled operation supplied evidence", err)
	}
	barrier := &debianProvenanceBarrierContext{Context: ctx, started: make(chan struct{}), release: make(chan struct{})}
	type evidence struct {
		raw         []byte
		observation DebianOutputObservation
		err         error
	}
	done := make(chan evidence, 1)
	joined, released := false, false
	go func() {
		raw, observation, err := receipt.localProducerProvenance(barrier)
		done <- evidence{raw, observation, err}
	}()
	defer func() {
		if !released {
			close(barrier.release)
		}
		if !joined {
			<-done
		}
	}()
	select {
	case <-barrier.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Err blocks admission after the opaque facade has been copied. Channels
	// order replacement against that copy without any caller data race.
	*receipt = DebianOutputReceipt{}
	close(barrier.release)
	released = true
	got := <-done
	joined = true
	if got.err != nil || string(got.raw) != string(raw) || got.observation != observation {
		t.Fatal("facade replacement changed original local provenance", got.err)
	}
	// The original issuer retains the opaque state; replacing its caller facade
	// did not revoke the Run-owned receipt.
	receipt = &DebianOutputReceipt{state: result.debian.receipts[output.ArtifactID]}
	if err := result.Close(); err != nil {
		t.Fatal(err)
	}
	if raw, zero, err := receipt.localProducerProvenance(t.Context()); raw != nil || zero != (DebianOutputObservation{}) || factoryErrorCode(err) != e.CodeEngineCapability {
		t.Fatal("closed original Run supplied evidence", err)
	}
}

type debianProvenanceBarrierContext struct {
	context.Context
	once             sync.Once
	started, release chan struct{}
}

func (ctx *debianProvenanceBarrierContext) Err() error {
	ctx.once.Do(func() { close(ctx.started); <-ctx.release })
	return ctx.Context.Err()
}

func TestDebianLocalProvenanceNativeForeignReceiptReplacement(t *testing.T) {
	if os.Getenv("NVKR_TEST_DEBIAN_CONTENT_RUNTIME") != "1" {
		t.Skip("explicit native provenance contract requires real composition capacity")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	plan, original, originalInspector, root := debianRunContentFixture(t, ctx, "issued")
	output := plan.LifecycleOutputs[0]
	if _, err := original.debian.inspectBoundProducer(ctx, original, plan, output, originalInspector); err != nil {
		t.Fatal(err)
	}
	first, err := original.debian.observeBoundOutput(ctx, original, plan, output)
	if err != nil {
		t.Fatal(err)
	}
	firstObservation, err := first.DescribeContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := plan.Digest()
	if err != nil {
		t.Fatal(err)
	}
	foreign := &LocalRunResult{SchemaVersion: LocalRunSchemaVersion, PlanDigest: digest, Qualification: TierLocalExecution}
	session, err := pipeline.NewNodeSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(session.Close)
	t.Cleanup(func() { _ = foreign.Close() })
	if err := bindDebianRunSession(ctx, plan, foreign, session); err != nil {
		t.Fatal(err)
	}
	runtimePath, err := os.ReadFile(filepath.Join(root, "producer-runtime-path"))
	if err != nil {
		t.Fatal(err)
	}
	if err := foreign.debian.executeBoundProducer(ctx, foreign, plan, output, string(runtimePath), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	workspace, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workspace.Close() })
	foreignInspector, err := OpenDiskEvidenceRuntime(ctx, plan, filepath.Join(root, "docker"), workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = foreignInspector.Close() })
	if _, err := foreign.debian.inspectBoundProducer(ctx, foreign, plan, output, foreignInspector); err != nil {
		t.Fatal(err)
	}
	second, err := foreign.debian.observeBoundOutput(ctx, foreign, plan, output)
	if err != nil {
		t.Fatal(err)
	}
	secondObservation, err := second.DescribeContext(ctx)
	if err != nil || secondObservation != firstObservation || second.state.owner == first.state.owner {
		t.Fatal("fixture requires equal bytes/Plan and distinct original owners", err)
	}
	barrier := &debianProvenanceBarrierContext{Context: ctx, started: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	joined, released := false, false
	go func() {
		raw, observed, err := first.localProducerProvenance(barrier)
		if raw != nil || observed != (DebianOutputObservation{}) {
			err = e.New(e.CodeInternal, "foreign replacement supplied original evidence")
		}
		done <- err
	}()
	defer func() {
		if !released {
			close(barrier.release)
		}
		if !joined {
			<-done
		}
	}()
	select {
	case <-barrier.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	*first = *second
	if err := originalInspector.Close(); err != nil {
		t.Fatal(err)
	}
	close(barrier.release)
	released = true
	err = <-done
	joined = true
	if factoryErrorCode(err) != e.CodeEngineCapability {
		t.Fatal("provenance followed a live foreign receipt after original inspector closed", err)
	}
	if raw, observed, err := second.localProducerProvenance(ctx); err != nil || len(raw) == 0 || observed != secondObservation {
		t.Fatal("foreign original Run was incorrectly revoked", err)
	}
}
