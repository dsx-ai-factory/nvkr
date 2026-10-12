// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package release

import (
	"bytes"
	"context"
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/trust"
)

// VerifyAuthenticatedReleaseSet checks exact authenticated release-set bytes,
// schema, canonical ordering and signer role. It grants no release clearance.
func VerifyAuthenticatedReleaseSet(ctx context.Context, document trust.DocumentReceipt) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if !document.Authenticated() || document.Role() != trust.RoleReleaseSet {
		return e.New(e.CodeSignatureInvalid,
			"release set requires an authenticated %s document", trust.RoleReleaseSet)
	}
	if document.SchemaVersion() != ReleaseSetSchemaVersion {
		return e.New(e.CodeInvalidCatalog,
			"authenticated release-set schemaVersion %q is unsupported (want %q)",
			document.SchemaVersion(), ReleaseSetSchemaVersion)
	}
	switch decoded := document.Decoded().(type) {
	case ReleaseSet:
	case *ReleaseSet:
		if decoded == nil {
			return e.New(e.CodeSignatureInvalid,
				"authenticated release set decoded to nil")
		}
	default:
		return e.New(e.CodeSignatureInvalid,
			"authenticated release set has unexpected decoded type")
	}
	raw := document.ExactBytes()
	var set ReleaseSet
	if err := canonical.UnmarshalExact(raw, &set); err != nil {
		return err
	}
	if err := set.Validate(); err != nil {
		return err
	}
	payload, err := set.Marshal()
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, payload) {
		return e.New(e.CodeInvalidCatalog,
			"authenticated release set does not use canonical member/artifact ordering")
	}
	signer := strings.TrimSpace(document.SignerIdentity())
	if signer == "" {
		return e.New(e.CodeSignatureInvalid,
			"authenticated release set has no signer identity")
	}
	return nil
}

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeTimeout, "nil context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "release document verification canceled")
	}
	return nil
}
