// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/google/go-containerregistry/pkg/name"
)

// These are transport descriptions selected from verified ordinary outputs, not
// substitutes for the lifecycle owner's later exact Run and payload capture.
type nativeImagePreload struct{ location, digest, file, reference string }

func nativeImageImportScript(file, reference string) (string, error) {
	ref, err := name.NewDigest(reference, name.StrictValidation)
	if err != nil || (ref.Name() != reference && reference != WorkloadImage) || !canonical.IsDigest(ref.DigestStr()) {
		return "", errors.New("native preload requires an exact canonical image digest reference")
	}
	options := ""
	// Preserve the existing Docker Hub workload alias. The pinned reference
	// parser normalizes docker.io to index.docker.io; OVN uses no such alias.
	if reference != WorkloadImage {
		options = "--digests --base-name " + operatorQuote(ref.Repository.Name()) + " "
	}
	return "ctr --address /run/containerd/containerd.sock --namespace k8s.io --timeout 120s images import " + options + "--platform linux/arm64 --label io.cri-containerd.image=managed " + operatorQuote(file) +
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
	return "curl --fail --silent --show-error --connect-timeout 10 --max-time 120 " + operatorQuote(image.location) + " -o " + operatorQuote(image.file) +
		"; printf '%s  %s\n' " + operatorQuote(strings.TrimPrefix(image.digest, "sha256:")) + " " + operatorQuote(image.file) + " | sha256sum -c -; " + imported, nil
}

// Project only the selected chart's existing image interface. The package's
// network/security settings remain source-owned. These two schemas are already
// admitted separately by siteOVNValuesForNetwork and the maintained catalogs.
func nativeOVNValues(version, reference string) (string, error) {
	ref, err := name.NewDigest(reference, name.StrictValidation)
	if err != nil || ref.Name() != reference || ref.Repository.Name() != "localhost/nvke/ovn-kubernetes" || !canonical.IsDigest(ref.DigestStr()) {
		return "", errors.New("native OVN requires the exact selected local manifest reference")
	}
	var values map[string]any
	switch version {
	case siteOVNVersion:
		values = map[string]any{"global": map[string]any{"skipCallToK8s": true, "imagePullSecretName": "", "useImageDigest": true,
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
