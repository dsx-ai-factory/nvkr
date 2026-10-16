// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"sync"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// localQualificationConsumer owns a fresh verification of retained bytes. It
// accepts no producer receipts or private keys, and makes no registry claim.
type localQualificationConsumer struct {
	*localConsumerState
}

type localConsumerState struct {
	qualified *localVerifiedQualification
	once      sync.Once
	closeErr  error
}

func (consumer *localQualificationConsumer) Close() error {
	if consumer == nil || consumer.localConsumerState == nil {
		return nil
	}
	state := consumer.localConsumerState
	state.once.Do(func() {
		state.closeErr = errors.Join(state.qualified.Close(), state.qualified.smoke.Close(), state.qualified.build.Close())
	})
	return state.closeErr
}

// openLocalQualificationConsumer verifies a complete evidence file set against
// an independently selected policy and independently reopened attempt payloads.
// The caller owns transport; neither the file set nor workspace supplies trust.
func openLocalQualificationConsumer(ctx context.Context, workspace string, policy LocalQualificationPolicy, binary string, files map[string][]byte) (result *localQualificationConsumer, err error) {
	if ctx == nil || workspace == "" {
		return nil, e.New(e.CodeUsage, "local consumer requires context and retained workspace")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "local consumer canceled")
	}
	frozen, err := freezeLocalEvidenceFiles(files)
	if err != nil {
		return nil, err
	}
	build, err := openLocalVerifiedBuild(ctx, workspace, policy, binary, frozen["build-evidence.json"], frozen["build-evidence.sig"])
	if err != nil {
		return nil, err
	}
	return finishLocalQualificationConsumer(ctx, build, binary, frozen)
}

// Takes ownership of build on every return path.
func finishLocalQualificationConsumer(ctx context.Context, build *localVerifiedBuild, binary string, frozen map[string][]byte) (result *localQualificationConsumer, err error) {
	defer func() {
		if err != nil {
			err = errors.Join(err, build.Close())
			result = nil
		}
	}()
	smoke, err := verifyLocalSmoke(ctx, build, binary, frozen["tool-smoke.json"], frozen["tool-smoke.sig"])
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, smoke.Close())
		}
	}()
	qualified, err := verifyLocalQualification(ctx, build, smoke, binary, frozen["qualification.json"], frozen["qualification.sig"])
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, qualified.Close())
		}
	}()
	expected, err := qualified.evidenceFiles(ctx)
	if err != nil {
		return nil, err
	}
	if len(expected) != len(frozen) {
		return nil, e.New(e.CodeReleaseIncomplete, "local evidence file set differs from verified attempt")
	}
	names := make([]string, 0, len(expected))
	for name := range expected {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !bytes.Equal(expected[name], frozen[name]) {
			return nil, e.New(e.CodeVerifyFailed, "local evidence file %s differs from verified attempt", name)
		}
	}
	return &localQualificationConsumer{&localConsumerState{qualified: qualified}}, nil
}

func freezeLocalEvidenceFiles(files map[string][]byte) (map[string][]byte, error) {
	limits := map[string]int64{
		"build-evidence.json": 4 << 20, "build-evidence.sig": 64 << 10,
		"tool-smoke.json": 4 << 20, "tool-smoke.sig": 64 << 10,
		"qualification.json": 4 << 20, "qualification.sig": 64 << 10,
		"plan.json": factory.MaxPlanBytes, "lifecycle.json": MaxRunRecordBytes,
		"compile-inputs.json": 64 << 20, "disk-materials.json": 4 << 20,
	}
	if len(files) < 9 || len(files) > len(limits) {
		return nil, e.New(e.CodeReleaseIncomplete, "local evidence file set is incomplete or has extra members")
	}
	for name := range files {
		if _, ok := limits[name]; !ok {
			return nil, e.New(e.CodeReleaseIncomplete, "unknown local evidence file")
		}
	}
	names := make([]string, 0, len(limits))
	for name := range limits {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		raw, present := files[name]
		if name == "disk-materials.json" && !present {
			continue
		}
		if !present {
			return nil, e.New(e.CodeReleaseIncomplete, "missing local evidence file %s", name)
		}
		if len(raw) == 0 || int64(len(raw)) > limits[name] {
			return nil, e.New(e.CodeVerifyFailed, "local evidence file %s is empty or oversized", name)
		}
	}
	frozen := make(map[string][]byte, len(files))
	for _, name := range names {
		if raw, ok := files[name]; ok {
			frozen[name] = bytes.Clone(raw)
		}
	}
	return frozen, nil
}
