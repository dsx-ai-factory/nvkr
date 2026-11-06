// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package storetest checks that a resultspublish.ObjectStore keeps the create-only
// contract Publish relies on. Every store runs the same suite.
package storetest

import (
	"bytes"
	"errors"
	"testing"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/resultspublish"
)

// Conformance runs the contract against a fresh store from open for each case.
func Conformance(t *testing.T, open func(t *testing.T) resultspublish.ObjectStore) {
	t.Helper()
	const path = "groups/nvkr/nvke/nvke-m2/0000000000000000001-101-1-202-1/artifacts/junit.xml"
	first, second := []byte("first"), []byte("second")
	t.Run("a created object reads back", func(t *testing.T) {
		store := open(t)
		if err := store.Create(t.Context(), path, first); err != nil {
			t.Fatal(err)
		}
		got, err := store.Read(t.Context(), path, resultspublish.MaxReadBytes)
		if err != nil || !bytes.Equal(got, first) {
			t.Fatalf("read %q, %v", got, err)
		}
	})
	t.Run("an existing object is never replaced", func(t *testing.T) {
		store := open(t)
		if err := store.Create(t.Context(), path, first); err != nil {
			t.Fatal(err)
		}
		if err := store.Create(t.Context(), path, second); !errors.Is(err, resultspublish.ErrExists) {
			t.Fatalf("second create: %v, want ErrExists", err)
		}
		if got, err := store.Read(t.Context(), path, resultspublish.MaxReadBytes); err != nil || !bytes.Equal(got, first) {
			t.Fatalf("read %q, %v", got, err)
		}
	})
	t.Run("a read is bounded", func(t *testing.T) {
		store := open(t)
		if err := store.Create(t.Context(), path, first); err != nil {
			t.Fatal(err)
		}
		requireCoded(t, func() error { _, err := store.Read(t.Context(), path, int64(len(first)-1)); return err }())
	})
	t.Run("a missing object does not read", func(t *testing.T) {
		requireCoded(t, func() error { _, err := open(t).Read(t.Context(), path, resultspublish.MaxReadBytes); return err }())
	})
}

func requireCoded(t *testing.T, err error) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) {
		t.Fatalf("got %v, want a coded error", err)
	}
}
