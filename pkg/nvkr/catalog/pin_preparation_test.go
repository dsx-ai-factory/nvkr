// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestPreparedSourcePinsWithholdAndBindInputs(t *testing.T) {
	dir := sourceUpdateFixture(t)
	before := readPinFixtureLock(t, dir)
	registry, err := os.ReadFile(filepath.Join(dir, RegistryFile))
	if err != nil {
		t.Fatal(err)
	}
	prepared, update, err := prepareSourcePins(t.Context(), SourcePinRequest{Directory: dir, Sources: []string{"source"}, Tag: "v1.1.0", Check: true}, nil,
		func(_ context.Context, _ Source, tag string) (Pin, error) { return sourceUpdatePin(tag), nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if update.Changed != 1 || !bytes.Equal(before, readPinFixtureLock(t, dir)) || !prepared.MatchesInputs(registry, before) {
		t.Fatal("preparation did not retain the unchanged input snapshot")
	}
	if prepared.MatchesInputs(append(registry, ' '), before) || prepared.MatchesInputs(registry, append(before, ' ')) {
		t.Fatal("preparation accepted different bytes")
	}
	raw := prepared.CandidateBytes()
	raw[0] = '!'
	if bytes.Equal(raw, prepared.CandidateBytes()) {
		t.Fatal("candidate bytes alias receipt")
	}
}

func TestPreparedSourcePinsRejectsUnissuedReceipt(t *testing.T) {
	var prepared PreparedSourcePins
	if prepared.MatchesInputs(nil, nil) || prepared.CandidateBytes() != nil {
		t.Fatal("unissued preparation provided a candidate")
	}
}
