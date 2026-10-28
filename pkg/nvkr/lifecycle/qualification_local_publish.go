// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/qualificationprogress"
	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
)

// References record attempted immutable destinations, including on failure.
// They grant no acceptance authority; only the returned consumer establishes it.
type localPublicationReport struct {
	Ordinary string            `json:"ordinary,omitempty"`
	Disks    map[string]string `json:"disks"`
	Evidence string            `json:"evidence,omitempty"`
}

func publishLocalQualification(ctx context.Context, qualified *localVerifiedQualification, repository, scratch, binary string, options delivery.RegistryOptions) (report localPublicationReport, consumer *localQualificationConsumer, err error) {
	report.Disks = map[string]string{}
	if qualified == nil {
		return report, nil, e.New(e.CodeGateUnmet, "local publication requires a verified gate")
	}
	operation, finish, err := qualified.build.beginOperation(ctx)
	if err != nil {
		return report, nil, err
	}
	defer finish()
	operation, cancel := context.WithTimeout(operation, 2*time.Hour)
	defer cancel()
	if err := qualified.requireLive(operation); err != nil {
		return report, nil, err
	}
	if err := options.Validate(); err != nil {
		return report, nil, err
	}
	names := make([]string, 0, len(qualified.build.capture.disks.members))
	for disk := range qualified.build.capture.disks.members {
		names = append(names, disk)
	}
	sort.Strings(names)
	if err := validateLocalPublicationTargets(repository, names, options); err != nil {
		return report, nil, err
	}
	if err := admitLocalPublicationScratch(operation, qualified.build, scratch); err != nil {
		return report, nil, err
	}
	activity := localProgress(operation)
	// Admit the actual consumer verifier before publication can leave remote bytes.
	checked, err := verifyLocalQualification(operation, qualified.build, qualified.smoke, binary, []byte(qualified.raw), []byte(qualified.signature))
	if err != nil {
		return report, nil, err
	}
	if err := checked.Close(); err != nil {
		return report, nil, err
	}
	stage, err := os.MkdirTemp(scratch, "nvkr-local-publication-*")
	if err != nil {
		return report, nil, e.Wrap(e.CodeImmutableViolation, err, "creating local publication workspace")
	}
	defer func() {
		if cleanupErr := os.RemoveAll(stage); cleanupErr != nil {
			err = errors.Join(err, e.Wrap(e.CodeImmutableViolation, cleanupErr, "removing local publication workspace"))
		}
		if err != nil && consumer != nil {
			err = errors.Join(err, consumer.Close())
			consumer = nil
		}
	}()
	ordinary := ""
	completed, total := 0, len(names)+1 // Every disk and the evidence carrier.
	if qualified.build.capture.ordinary != nil {
		total++
	}
	if qualified.build.capture.ordinary != nil {
		activity.startItem("ordinary-artifacts", completed, total)
		ordinary = filepath.Join(stage, "ordinary-upload")
		if err := qualified.build.capture.ordinary.WriteLayout(operation, ordinary); err != nil {
			return report, nil, err
		}
		report.Ordinary = repository + "/ordinary@" + qualified.build.capture.ordinaryRoot
		ref, pushErr := delivery.Push(operation, ordinary, repository+"/ordinary", options)
		if pushErr != nil {
			return report, nil, pushErr
		}
		if ref != report.Ordinary {
			return report, nil, e.New(e.CodeVerifyFailed, "ordinary publication changed immutable identity")
		}
		activity.finishItem()
		completed++
	}
	for _, disk := range names {
		activity.startItem(disk, completed, total)
		owner := qualified.build.capture.disks.members[disk]
		report.Disks[disk] = repository + "/disk-" + disk + "@" + owner.Inspection().ManifestDigest
		ref, pushErr := delivery.PushDiskArtifact(operation, owner, repository+"/disk-"+disk, stage, options)
		if pushErr != nil {
			return report, nil, pushErr
		}
		if ref != report.Disks[disk] {
			return report, nil, e.New(e.CodeVerifyFailed, "disk publication changed immutable identity")
		}
		activity.finishItem()
		completed++
	}
	activity.startItem("signed-evidence", completed, total)
	report.Evidence, err = qualified.pushEvidence(operation, repository+"/evidence", options)
	if err != nil {
		return report, nil, err
	}
	activity.finishItem()
	activity.finish()
	activity.start(qualificationprogress.StageReadback)
	completed, total = 0, total+1 // Fresh consumer verification is a separate item.
	activity.startItem("signed-evidence", completed, total)
	files, err := pullLocalEvidence(operation, report.Evidence, options)
	if err != nil {
		return report, nil, err
	}
	activity.finishItem()
	completed++
	if ordinary != "" {
		activity.startItem("ordinary-artifacts", completed, total)
		ordinary = filepath.Join(stage, "ordinary-download")
		if _, err := delivery.Pull(operation, report.Ordinary, ordinary, options); err != nil {
			return report, nil, err
		}
		activity.finishItem()
		completed++
	}
	disks := map[string]string{}
	for _, disk := range names {
		activity.startItem(disk, completed, total)
		view := qualified.build.capture.disks.members[disk].Inspection()
		disks[disk] = filepath.Join(stage, "disk-"+disk)
		if err := delivery.PullDiskArtifact(operation, report.Disks[disk], disks[disk], view.Platform, view.Size, options); err != nil {
			return report, nil, err
		}
		activity.finishItem()
		completed++
	}
	activity.startItem("independent-consumer", completed, total)
	consumer, err = openLocalLayoutConsumer(operation, ordinary, disks, qualified.build.policy, binary, files)
	if err != nil {
		return report, nil, err
	}
	if err := qualified.requireLive(operation); err != nil {
		return report, consumer, err
	}
	activity.finishItem()
	return report, consumer, nil
}

func admitLocalPublicationScratch(ctx context.Context, build *localVerifiedBuild, scratch string) error {
	if scratch == "" {
		return e.New(e.CodeUsage, "local publication requires explicit disk-backed scratch")
	}
	// Export, download and consumer materialization may coexist. This is a
	// conservative capacity observation, not a reservation of physical storage.
	required := uint64(1 << 30)
	add := func(size uint64) error {
		if size > (math.MaxUint64-required)/3 {
			return e.New(e.CodeEngineCapability, "local publication storage requirement overflows")
		}
		required += size * 3
		return nil
	}
	if build.capture.ordinary != nil {
		size, err := build.capture.ordinary.StoredBytes(ctx)
		if err != nil {
			return err
		}
		if err := add(size); err != nil {
			return err
		}
	}
	for _, disk := range build.capture.disks.members {
		size := disk.Inspection().Size
		if size <= 0 {
			return e.New(e.CodeVerifyFailed, "invalid local disk storage size")
		}
		if err := add(uint64(size)); err != nil {
			return err
		}
	}
	parent, _, err := securepath.OpenDirectory(scratch)
	if err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "opening local publication scratch")
	}
	defer parent.Close()
	available, err := securepath.AvailableDiskBackedBytes(ctx, parent)
	if err != nil {
		return e.Wrap(e.CodeEngineCapability, err, "observing local publication scratch")
	}
	if available < required {
		return e.New(e.CodeEngineCapability, "local publication needs %d scratch bytes, only %d available", required, available)
	}
	return nil
}

func validateLocalPublicationTargets(repository string, names []string, options delivery.RegistryOptions) error {
	parse := []name.Option{name.StrictValidation}
	if options.PlainHTTP {
		parse = append(parse, name.Insecure)
	}
	repositories := []string{repository, repository + "/ordinary", repository + "/evidence"}
	for _, disk := range names {
		repositories = append(repositories, repository+"/disk-"+disk)
	}
	for _, target := range repositories {
		repo, parseErr := name.NewRepository(target, parse...)
		if parseErr != nil || repo.Name() != target || strings.ContainsAny(target[strings.LastIndex(target, "/")+1:], ":@") {
			return e.New(e.CodeUsage, "local publication requires canonical untagged repositories")
		}
	}
	return nil
}
