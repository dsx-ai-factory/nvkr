// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package tekton

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const CarrierSchema = "nvkr.dev/tekton-carriers/v1"
const MaxManifestBytes = 1 << 20

// Carrier describes an exact plain TAR blob. File names are assigned by sorted
// output membership, never by provider or artifact-controlled path text.
type Carrier struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Size   int64  `json:"size"`
}

// CarrierManifest is untrusted until authenticated against a successful task
// and the complete Plan-derived producer output set.
type CarrierManifest struct {
	SchemaVersion string    `json:"schemaVersion"`
	PlanDigest    string    `json:"planDigest"`
	Carriers      []Carrier `json:"carriers"`
}

func CarrierFile(index int) string { return fmt.Sprintf("carrier-%04d.tar", index) }

type CarrierSet struct{ members map[string]carrierMember }
type carrierMember struct {
	carrier Carrier
	file    string
}

// AuthenticateCarriers checks exact producer membership before any filesystem
// access. Equal blob digests do not authorize another named output or path.
func AuthenticateCarriers(ctx context.Context, receipt *TaskReceipt, raw []byte, expected map[string]string) (*CarrierSet, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > MaxManifestBytes || len(expected) > 4096 {
		return nil, e.New(e.CodeVerifyFailed, "Tekton carrier manifest exceeds its bounds")
	}
	if err := receipt.RequireBytes(ctx, "carrier", raw); err != nil {
		return nil, err
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "invalid Tekton carrier manifest")
	}
	var manifest CarrierManifest
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return nil, e.Wrap(e.CodeVerifyFailed, err, "decoding Tekton carrier manifest")
	}
	if manifest.SchemaVersion != CarrierSchema || manifest.PlanDigest != receipt.planDigest || len(manifest.Carriers) != len(expected) {
		return nil, e.New(e.CodeVerifyFailed, "Tekton carrier manifest does not match the exact Plan output set")
	}
	set := &CarrierSet{members: make(map[string]carrierMember, len(expected))}
	previous := ""
	for index, carrier := range manifest.Carriers {
		path, ok := expected[carrier.ID]
		if !ok || carrier.ID <= previous || carrier.Path != path || !securepath.WorkspaceRelative(path) || !canonical.IsDigest(carrier.Digest) || carrier.Size < 0 || carrier.Size > transportarchive.MaxArchiveBytes {
			return nil, e.New(e.CodeVerifyFailed, "Tekton carrier substituted, repeated or omitted a named output")
		}
		set.members[carrier.ID] = carrierMember{carrier, CarrierFile(index)}
		previous = carrier.ID
	}
	return set, nil
}

// Restore authenticates each complete archive stream before publishing its
// roots through the shared transport owner. Partial failures cannot issue an
// executable receipt; callers must stop the consuming operation on any error.
func (set *CarrierSet) Restore(ctx context.Context, directory, workspace string, selected []string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if set == nil {
		return e.New(e.CodeVerifyFailed, "Tekton restore requires authenticated carrier membership")
	}
	ids := append([]string(nil), selected...)
	sort.Strings(ids)
	for index, id := range ids {
		if _, ok := set.members[id]; !ok || (index > 0 && id == ids[index-1]) {
			return e.New(e.CodeVerifyFailed, "Tekton restore selected an unknown or repeated output")
		}
	}
	for _, id := range ids {
		member := set.members[id]
		file, info, err := securepath.OpenRegular(filepath.Join(directory, member.file))
		if err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "opening exact Tekton carrier")
		}
		if info.Size() != member.carrier.Size {
			_ = file.Close()
			return e.New(e.CodeVerifyFailed, "Tekton carrier size differs from authenticated manifest")
		}
		err = transportarchive.UnpackStream(ctx, file, member.carrier.Size, member.carrier.Digest, workspace, []string{member.carrier.Path})
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return e.Wrap(e.CodeVerifyFailed, closeErr, "closing Tekton carrier")
		}
	}
	return nil
}
