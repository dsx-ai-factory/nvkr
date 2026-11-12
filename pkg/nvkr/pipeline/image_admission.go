// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/name"
)

// ImageAccess describes materialization permission, not proof of image bytes.
// Private credentials belong to a separate preparation phase; this owner never
// accepts them. Empty and unknown routes fail admission.
type ImageAccess string

const (
	ImagePreparedOnly  ImageAccess = "prepared-only"
	ImageAnonymous     ImageAccess = "anonymous"
	ImageScopedReader  ImageAccess = "scoped-reader"
	ImageMeasuredLocal ImageAccess = "measured-local-image"
)

// PreparedImage is a process-local, point-in-time admission receipt. Its private
// fields bind the exact subject, platform, local config ID and runtime identity.
// It is not qualification evidence or a continuing grant to a daemon's store.
type PreparedImage struct {
	subject, platform, runtimeIdentity, digest, configID string
}

type imageOutput func(context.Context, ...string) ([]byte, error)

// PrepareContainerImage authenticates local bytes first. Only an explicitly
// anonymous route can materialize a missing image. A present invalid image never
// triggers a pull, and private misses require a new scoped preparation phase.
func PrepareContainerImage(ctx context.Context, runtimeBinary, image, platform string, access ImageAccess) (receipt *PreparedImage, returnErr error) {
	if err := requireRunContext(ctx, "image admission"); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	defer func() {
		returnErr = mapCancellation(ctx, returnErr, "image admission")
		if returnErr != nil {
			receipt = nil
		}
	}()
	if err := validateImageAccess(image, platform, access); err != nil {
		return nil, err
	}
	if name := filepath.Base(runtimeBinary); name != "docker" && name != "podman" {
		return nil, e.New(e.CodeEngineCapability, "image admission requires Docker or Podman")
	}
	output := func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] != "pull" {
			return containerPlatformOutput(ctx, runtimeBinary, args...)
		}
		cmd := execCmd(RunOptions{Ctx: ctx}, runtimeBinary, args...)
		var diagnostic containerPlatformBuffer
		cmd.Stdout, cmd.Stderr = io.Discard, &diagnostic
		if err := runOwnedCommand(ctx, cmd); err != nil {
			return nil, containerPullFailure(ctx, platform, err, &diagnostic)
		}
		return nil, nil
	}
	identity, err := containerImageRuntimeIdentity(ctx, runtimeBinary, platform, output)
	if err != nil {
		return nil, err
	}
	return admitContainerImage(ctx, image, platform, access, identity, output)
}

// RequireFor reauthenticates without network access. Callers must additionally
// launch with --pull=never so eviction cannot become implicit materialization.
func (image *PreparedImage) RequireFor(ctx context.Context, runtimeBinary, subject, platform string) error {
	if image == nil || image.runtimeIdentity == "" || image.subject != subject || image.platform != platform {
		return e.New(e.CodePreflightFailed, "prepared image receipt does not bind this subject and platform")
	}
	observed, err := PrepareContainerImage(ctx, runtimeBinary, subject, platform, ImagePreparedOnly)
	if err != nil {
		return err
	}
	return image.requireObserved(observed)
}

func (image *PreparedImage) requireObserved(observed *PreparedImage) error {
	if image == nil || observed == nil || image.runtimeIdentity == "" || *image != *observed {
		return e.New(e.CodePreflightFailed, "prepared image or runtime changed after admission")
	}
	return nil
}

func validateImageAccess(image, platform string, access ImageAccess) error {
	if platform != "linux/amd64" && platform != "linux/arm64" {
		return e.New(e.CodeEngineCapability, "container execution platform %q is unsupported", platform)
	}
	switch access {
	case ImagePreparedOnly, ImageAnonymous, ImageScopedReader, ImageMeasuredLocal:
	default:
		return e.New(e.CodeEngineCapability, "unsupported image access route %q", access)
	}
	local := canonical.IsDigest(image)
	if access == ImageMeasuredLocal && !local || local && (access == ImageAnonymous || access == ImageScopedReader) {
		return e.New(e.CodeEngineCapability, "image source differs from admitted access route")
	}
	if !local {
		if _, err := name.NewDigest(image); err != nil {
			return e.Wrap(e.CodeUnpinnedSource, err, "image admission requires a registry subject")
		}
		if _, err := imageManifestDigest(image); err != nil {
			return e.Wrap(e.CodeUnpinnedSource, err, "image admission requires an immutable subject")
		}
	}
	return nil
}

func admitContainerImage(ctx context.Context, image, platform string, access ImageAccess, identity string, output imageOutput) (receipt *PreparedImage, returnErr error) {
	if err := requireRunContext(ctx, "image admission"); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	defer func() {
		returnErr = mapCancellation(ctx, returnErr, "image admission")
		if returnErr != nil {
			receipt = nil
		}
	}()
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "image admission canceled")
	}
	if err := validateImageAccess(image, platform, access); err != nil {
		return nil, err
	}
	if identity == "" {
		return nil, e.New(e.CodeEngineCapability, "image admission requires an observed runtime identity")
	}
	args := []string{"image", "inspect", "--format", "{{json .}}", image}
	raw, err := output(ctx, args...)
	if err != nil {
		if access != ImageAnonymous {
			return nil, e.New(e.CodeEngineCapability, "prepared image is unavailable; repeat admitted preparation")
		}
		if err := ctx.Err(); err != nil {
			return nil, e.Wrap(e.CodeTimeout, err, "image admission canceled")
		}
		if _, err := output(ctx, "pull", "--platform", platform, image); err != nil {
			return nil, e.Wrap(e.CodeEngineCapability, err, "materializing anonymous image")
		}
		raw, err = output(ctx, args...)
		if err != nil {
			return nil, e.New(e.CodeEngineCapability, "materialized image is unavailable")
		}
	}
	return inspectAdmittedImage(image, platform, identity, raw)
}

func inspectAdmittedImage(image, platform, identity string, raw []byte) (*PreparedImage, error) {
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "decoding prepared image")
	}
	var inspection packerImageInspection
	if err := json.Unmarshal(raw, &inspection); err != nil {
		return nil, e.Wrap(e.CodeEngineCapability, err, "decoding prepared image")
	}
	if inspection.OS+"/"+inspection.Architecture != platform {
		return nil, e.New(e.CodePreflightFailed, "prepared image platform differs from %s", platform)
	}
	id := normalizeLocalImageID(inspection.ID)
	if validateSHA256(id) != nil {
		return nil, e.New(e.CodePreflightFailed, "prepared image has no canonical local config identity")
	}
	digest := image
	if canonical.IsDigest(image) {
		if id != image {
			return nil, e.New(e.CodePreflightFailed, "prepared local image ID differs from selected ID")
		}
	} else {
		found := false
		for _, ref := range inspection.RepoDigests {
			if sameImageSubject(ref, image) {
				found = true
			}
		}
		if !found {
			return nil, e.New(e.CodePreflightFailed, "runtime did not report exact prepared repository subject")
		}
		var err error
		digest, err = imageManifestDigest(image)
		if err != nil {
			return nil, e.Wrap(e.CodeUnpinnedSource, err, "prepared image subject")
		}
	}
	return &PreparedImage{subject: image, platform: platform, runtimeIdentity: identity, digest: digest, configID: id}, nil
}

// This unqualified adapter binds the measured executable and observed daemon.
// Qualified/held consumers supply their existing held runtime identity instead.
func containerImageRuntimeIdentity(ctx context.Context, runtimeBinary, platform string, output imageOutput) (string, error) {
	path, err := resolveExecutionRuntime(runtimeBinary)
	if err != nil {
		return "", err
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "resolving image runtime")
	}
	digest, _, _, err := hashRegularBeneathContext(ctx, filepath.Dir(path), filepath.Base(path))
	if err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "measuring image runtime")
	}
	raw, err := output(ctx, "info", "--format", "{{json .}}")
	if err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "observing image runtime")
	}
	if err := checkContainerDaemonPlatform(raw, platform); err != nil {
		return "", err
	}
	var serverID string
	if filepath.Base(runtimeBinary) == "podman" {
		serverID, err = podmanServerIdentity(raw)
	} else {
		var info struct {
			ID string `json:"ID"`
		}
		err = json.Unmarshal(raw, &info)
		serverID = info.ID
	}
	if err != nil || strings.TrimSpace(serverID) == "" {
		return "", e.New(e.CodeEngineCapability, "image runtime has no stable server identity")
	}
	version, err := output(ctx, "version", "--format", "{{json .}}")
	if err != nil {
		return "", e.Wrap(e.CodeEngineCapability, err, "observing image runtime version")
	}
	return runtimeIdentityFromOutput(digest, "", serverID, version)
}

// Runtime displays may elide Docker Hub's default registry/library prefix. These
// spellings name one repository subject; equal digests in other repositories do not.
func sameImageSubject(a, b string) bool {
	left, err := name.NewDigest(a)
	if err != nil {
		return false
	}
	right, err := name.NewDigest(b)
	return err == nil && left.Name() == right.Name()
}

// Held image and runtime-identity queries retain the sealed executable and its
// scrubbed environment, with bounded output and no raw child diagnostics.
func heldImageOutput(ctx context.Context, runtime *heldRuntimeExecutable, platform string, args ...string) ([]byte, error) {
	duration := 30 * time.Second
	if args[0] == "pull" {
		duration = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	var output, diagnostic containerPlatformBuffer
	if err := runtime.run(ctx, &output, &diagnostic, args...); err != nil {
		if args[0] == "pull" {
			return nil, containerPullFailure(ctx, platform, err, &diagnostic)
		}
		return nil, mapCancellation(ctx, e.Wrap(e.CodeEngineCapability, err, "observing held image runtime"), "observing held image runtime")
	}
	if output.overflow {
		return nil, e.New(e.CodeEngineCapability, "image runtime observation exceeds 1 MiB")
	}
	return output.buffer.Bytes(), nil
}
