// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"io"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// BootInventoryError retains the exact attempted inventory locator when commit,
// readback or subsequent cleanup fails. It is descriptive recovery input, never
// proof of publication. An absent pin means the provider acknowledgement was
// not observed; a present pin must never be replaced by current-version discovery.
type BootInventoryError struct {
	Attempt StoredObject
	Err     error
}

func (failure *BootInventoryError) Error() string {
	return "boot inventory completion failed: " + failure.Err.Error()
}
func (failure *BootInventoryError) Unwrap() error { return failure.Err }

// ValidateBootInventoryAttempt checks only canonical recovery descriptions.
// Recovery still reauthenticates every original proof and selected object.
func ValidateBootInventoryAttempt(reference string, attempt StoredObject) error {
	if err := ValidatePublicationReference(reference); err != nil {
		return err
	}
	if !isDigest(attempt.SHA256) || attempt.Key != bootInventoryKey(reference, attempt.SHA256) ||
		attempt.Size <= 0 || attempt.Size > maxMirrorInventoryBytes || attempt.MediaType != "application/json" {
		return e.New(e.CodeVerifyFailed, "boot recovery requires its exact attempted inventory object")
	}
	if attempt.VersionID != "" || attempt.ETag != "" {
		return validateBootPin(attempt)
	}
	return nil
}

func commitBootInventory(ctx context.Context, store ObjectStore, object Object, raw []byte, recovery *StoredObject) (_ StoredObject, resultErr error) {
	attempt := StoredObject{Object: object}
	defer func() {
		if resultErr != nil {
			resultErr = &BootInventoryError{Attempt: attempt, Err: resultErr}
		}
	}()
	if recovery != nil {
		attempt = *recovery
		if recovery.Object != object {
			return StoredObject{}, e.New(e.CodeVerifyFailed, "reauthenticated inventory differs from the exact attempted object")
		}
	}
	if attempt.VersionID == "" && attempt.ETag == "" {
		observed, err := store.PutIfAbsent(ctx, object, bytes.NewReader(raw))
		if err != nil {
			return StoredObject{}, e.Wrap(e.CodeVerifyFailed, err, "conditionally storing boot inventory")
		}
		if observed.Object != object {
			return StoredObject{}, e.New(e.CodeVerifyFailed, "boot inventory provider changed exact object")
		}
		if err := validateBootPin(observed); err != nil {
			return StoredObject{}, err
		}
		attempt = observed
	}
	if err := readMirrorObject(ctx, store, attempt, io.Discard); err != nil {
		return StoredObject{}, err
	}
	return attempt, nil
}
