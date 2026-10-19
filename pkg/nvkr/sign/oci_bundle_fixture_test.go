// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// These helpers consume independent cosign output only in the interoperability
// fixture. Production discovery uses the bounded lazy OCI reader.
func ociDownloadBundleArgs(ref, predicateType string) []string {
	return []string{"download", "attestation", "--predicate-type", predicateType, ref}
}

func decodeOCIBundles(raw []byte) ([][]byte, error) {
	if int64(len(raw)) > maxCosignOutputBytes {
		return nil, e.New(e.CodeSignatureInvalid, "bundle fixture exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var bundles [][]byte
	for {
		var document json.RawMessage
		if err := decoder.Decode(&document); err == io.EOF {
			break
		} else if err != nil {
			return nil, e.Wrap(e.CodeSignatureInvalid, err, "decoding bundle fixture")
		}
		if err := canonical.ValidateJSONDocument(document); err != nil {
			return nil, e.Wrap(e.CodeSignatureInvalid, err, "invalid bundle fixture")
		}
		bundles = append(bundles, document)
	}
	return bundles, nil
}

func fixtureOCIBundleSource(raw []byte) (*ociBundleSource, error) {
	bundles, err := decodeOCIBundles(raw)
	if err != nil {
		return nil, err
	}
	source := &ociBundleSource{close: func() {}}
	byDigest := make(map[string][]byte)
	for i, bundle := range bundles {
		// Synthetic descriptor keys preserve fixture order, including duplicate
		// payloads. Real transport independently authenticates descriptor bytes.
		digest := v1.Hash{Algorithm: "sha256", Hex: fmt.Sprintf("%064x", i+1)}
		typ, _ := ociBundlePredicateType(bundle)
		source.descriptors = append(source.descriptors, v1.Descriptor{Digest: digest, Annotations: map[string]string{ociPredicateAnnotation: typ}})
		byDigest[digest.String()] = bundle
	}
	source.load = func(_ context.Context, descriptor v1.Descriptor) ([]byte, error) {
		return byDigest[descriptor.Digest.String()], nil
	}
	return source, nil
}
