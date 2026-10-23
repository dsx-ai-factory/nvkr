// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/google/go-containerregistry/pkg/name"
)

// These are transport descriptions selected from verified ordinary outputs, not
// substitutes for the lifecycle owner's later exact Run and payload capture.
type nativeImagePreload struct{ location, digest, file, reference string }

// Guest preparation outlives its renderer. Carry the caller's absolute deadline
// into the guest, rather than resetting a duration when cloud-init gets to it.
// timeout owns the preparation process group and escalates termination after a
// bounded grace; it does not delete the VM or substitute for host-side readiness.
func guestWorkCommand(ctx context.Context, script string) (string, error) {
	if _, err := remainingWork(ctx); err != nil {
		return "", err
	}
	deadline, _ := ctx.Deadline()
	return fmt.Sprintf("set -eu; remaining=$((%d - $(date +%%s))); test \"$remaining\" -gt 0; exec timeout --kill-after=5s \"${remaining}s\" /bin/bash -ec %s", deadline.Unix(), operatorQuote(script)), nil
}

// Use the verified manifest as the import subject while retaining the repository
// spelling used in the archive. In particular, containerd uses docker.io aliases.
func nativeImageDigestReference(image NativeImageAsset) (string, error) {
	ref, err := name.ParseReference(image.Reference, name.StrictValidation)
	if err != nil || !nativeImageReferenceIsCanonical(ref, image.Reference) || !canonical.IsDigest(image.ManifestDigest) {
		return "", errors.New("site image requires a valid import alias and exact manifest digest")
	}
	if selected, ok := ref.(name.Digest); ok && selected.DigestStr() != image.ManifestDigest {
		return "", errors.New("site image import alias differs from its selected manifest")
	}
	repository := ref.Context().Name()
	if strings.HasPrefix(image.Reference, "docker.io/") {
		repository = "docker.io/" + ref.Context().RepositoryStr()
	}
	return repository + "@" + image.ManifestDigest, nil
}

func nativeImageReferenceIsCanonical(ref name.Reference, original string) bool {
	return ref.Name() == original || (strings.HasPrefix(ref.Name(), "index.docker.io/") && original == "docker.io/"+strings.TrimPrefix(ref.Name(), "index.docker.io/"))
}

func nativeImageImportScript(file, reference string) (string, error) {
	ref, err := name.NewDigest(reference, name.StrictValidation)
	if err != nil {
		return "", errors.New("native preload requires an exact canonical image digest reference")
	}
	if !nativeImageReferenceIsCanonical(ref, reference) || !canonical.IsDigest(ref.DigestStr()) {
		return "", errors.New("native preload requires an exact canonical image digest reference")
	}
	// Import the selected child manifest directly. CRI must add its managed
	// label after creating the config-ID alias used by image-volume mounts;
	// pre-setting that label suppresses alias creation in containerd.
	repository := strings.TrimSuffix(reference, "@"+ref.DigestStr())
	// The enclosing guest preparation command enforces the caller deadline.
	return "ctr --address /run/containerd/containerd.sock --namespace k8s.io --timeout 0s images import --local --digests --base-name " + operatorQuote(repository) + " --platform linux/arm64 " + operatorQuote(file) +
		"; crictl --runtime-endpoint unix:///run/containerd/containerd.sock --image-endpoint unix:///run/containerd/containerd.sock --timeout 10s inspecti --quiet " + operatorQuote(reference) + " >/dev/null", nil
}

func nativeImagePreloadScript(image nativeImagePreload) (string, error) {
	if !canonical.IsDigest(image.digest) || !configString(image.location) {
		return "", errors.New("native preload requires exact transport integrity and location")
	}
	imported, err := nativeImageImportScript(image.file, image.reference)
	if err != nil {
		return "", err
	}
	return "curl --fail --silent --show-error --connect-timeout 10 " + operatorQuote(image.location) + " -o " + operatorQuote(image.file) +
		"; printf '%s  %s\n' " + operatorQuote(strings.TrimPrefix(image.digest, "sha256:")) + " " + operatorQuote(image.file) + " | sha256sum -c -; " + imported, nil
}

// Project the selected chart's exact image binding and native deployment settings.
// The maintained catalogs admit the two chart schemas; the NVIDIA lane
// preserves NVKE's hostPort owner.
func nativeOVNValues(version, reference string) (string, error) {
	ref, err := name.NewDigest(reference, name.StrictValidation)
	if err != nil || ref.Name() != reference || ref.Repository.Name() != "localhost/nvke/ovn-kubernetes" || !canonical.IsDigest(ref.DigestStr()) {
		return "", errors.New("native OVN requires the exact selected local manifest reference")
	}
	var values map[string]any
	switch version {
	case siteOVNVersion:
		// The pinned chart must read the actual kube-dns Service during install.
		// Skipping lookup selects its fixed DNS address even for a different service CIDR.
		// Replacement values must also preserve the VM package's hostPort enablement.
		values = map[string]any{"global": map[string]any{"skipCallToK8s": false, "enablePodHostPort": true, "imagePullSecretName": "", "useImageDigest": true,
			"image": map[string]any{"repository": ref.Repository.Name(), "tag": "nvkr", "sha256": strings.TrimPrefix(ref.DigestStr(), "sha256:")}}}
	case upstreamSiteOVNVersion:
		values = map[string]any{"skipCallToK8s": true, "global": map[string]any{"enableOvnKubeIdentity": false,
			"image": map[string]any{"repository": ref.Repository.Name() + "@sha256", "tag": strings.TrimPrefix(ref.DigestStr(), "sha256:"), "pullPolicy": "IfNotPresent"}}}
	default:
		return "", errors.New("native OVN image projection requires a supported selected chart")
	}
	// Release CRDs count raw Go-template delimiters before rendering. Keep JSON
	// closing braces separated so chart values cannot add a literal }} token.
	raw, err := json.MarshalIndent(values, "", "  ")
	return string(raw), err
}

func nativeOVNValuesScript(version, reference string) (string, error) {
	values, err := nativeOVNValues(version, reference)
	if err != nil {
		return "", err
	}
	return "test -f /etc/dgx/ovn-kubernetes/Chart.yaml; printf '%s\n' " + operatorQuote(values) + " > /etc/dgx/ovn-kubernetes/values-nke.yaml", nil
}
