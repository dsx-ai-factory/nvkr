// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package qualificationprogress carries bounded descriptive local qualification
// activity. Events cannot establish artifact, signature, or release authority.
package qualificationprogress

import (
	"strings"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const (
	KindStage      = "stage"
	KindItem       = "item"
	StageInputs    = "inputs"
	StageBuilder   = "builder"
	StageSmoke     = "smoke"
	StageQualifier = "qualifier"
	StagePublish   = "publish"
	StageReadback  = "readback"
	StateRunning   = "running"
	StateCompleted = "completed"
	StateFailed    = "failed"
	MaxRecordBytes = 1024
	MaxItems       = 16384
)

// Event names a source-owned boundary or public output/tool identifier. Counts
// describe observed items, never percentages, remaining time, or acceptance.
// Producers must supply public names; this codec does not redact arbitrary text.
type Event struct {
	Kind      string `json:"kind"`
	Stage     string `json:"stage"`
	State     string `json:"state"`
	Item      string `json:"item,omitempty"`
	Completed int    `json:"completed,omitempty"`
	Total     int    `json:"total,omitempty"`
}

func (event Event) Validate() error {
	switch event.Stage {
	case StageInputs, StageBuilder, StageSmoke, StageQualifier, StagePublish, StageReadback:
	default:
		return e.New(e.CodeUsage, "unknown local qualification progress stage")
	}
	if event.State != StateRunning && event.State != StateCompleted && event.State != StateFailed {
		return e.New(e.CodeUsage, "unknown local qualification progress state")
	}
	if event.Completed < 0 || event.Total < 0 || event.Completed > event.Total || event.Total > MaxItems {
		return e.New(e.CodeUsage, "invalid local qualification progress counts")
	}
	switch event.Kind {
	case KindStage:
		if event.Item != "" {
			return e.New(e.CodeUsage, "qualification stage progress cannot name an item")
		}
	case KindItem:
		if !safeItem(event.Item) || event.Total == 0 {
			return e.New(e.CodeUsage, "invalid local qualification progress item")
		}
	default:
		return e.New(e.CodeUsage, "unknown local qualification progress kind")
	}
	return nil
}

func safeItem(item string) bool {
	if item == "" || len(item) > 256 {
		return false
	}
	for _, part := range strings.Split(item, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	for _, char := range item {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("-_./", char)) {
			return false
		}
	}
	return true
}

func Decode(raw []byte) (Event, error) {
	var event Event
	if len(raw) == 0 || len(raw) >= MaxRecordBytes {
		return event, e.New(e.CodeUsage, "qualification progress record exceeds its bound")
	}
	if err := canonical.UnmarshalExact(raw, &event); err != nil {
		return Event{}, e.New(e.CodeUsage, "malformed local qualification progress")
	}
	if err := event.Validate(); err != nil {
		return Event{}, err
	}
	return event, nil
}
