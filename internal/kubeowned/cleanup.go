// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package kubeowned joins deletion of previously observed Kubernetes UIDs.
// Callers own authenticated API access and the binding's admission.
package kubeowned

import (
	"context"
	"errors"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type Binding struct{ Path, UID string }
type Observation struct {
	UID      string
	Deleting bool
}

// Delete preserves the existing conformance cleanup state machine. Bindings
// are supplied in deletion order, and every DELETE carries its observed UID.
func Delete(ctx context.Context, bindings []Binding,
	get func(context.Context, string) (Observation, error),
	deleteObject func(context.Context, string, []byte) error, interval time.Duration) error {
	var firstDeleteErr error
	deleting := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return errors.Join(firstDeleteErr, e.Wrap(e.CodeTimeout, err, "waiting for terminal cleanup"))
		}
		for _, ref := range bindings {
			if ref.UID == "" {
				continue
			}
			found, err := get(ctx, ref.Path)
			if err != nil {
				return errors.Join(firstDeleteErr, err)
			}
			if found.UID == "" {
				continue
			}
			if found.UID != ref.UID {
				return errors.Join(firstDeleteErr, e.New(e.CodeVerifyFailed, "attempt resource replaced; cleanup rejected"))
			}
			if deleting[ref.Path] || found.Deleting {
				deleting[ref.Path] = true
				continue
			}
			body, err := canonical.Marshal(struct {
				APIVersion    string `json:"apiVersion"`
				Kind          string `json:"kind"`
				Preconditions struct {
					UID string `json:"uid"`
				} `json:"preconditions"`
			}{APIVersion: "v1", Kind: "DeleteOptions", Preconditions: struct {
				UID string `json:"uid"`
			}{UID: ref.UID}})
			if err != nil {
				return errors.Join(firstDeleteErr, e.New(e.CodeVerifyFailed, "encoding UID cleanup precondition"))
			}
			if err = deleteObject(ctx, ref.Path, body); err != nil {
				current, getErr := get(ctx, ref.Path)
				if getErr == nil && current.UID == "" {
					continue
				}
				if getErr != nil {
					return errors.Join(firstDeleteErr, err, getErr)
				}
				if current.UID != ref.UID {
					return errors.Join(firstDeleteErr, err, e.New(e.CodeVerifyFailed, "attempt resource replaced; cleanup rejected"))
				}
				first := firstDeleteErr == nil
				if first {
					firstDeleteErr = err
				}
				if ctx.Err() != nil {
					return errors.Join(firstDeleteErr, e.Wrap(e.CodeTimeout, ctx.Err(), "waiting for terminal cleanup"))
				}
				if current.Deleting {
					deleting[ref.Path] = true
					continue
				}
				var coded *e.Error
				if !errors.As(err, &coded) || (coded.Code != e.CodeUnavailable && coded.Code != e.CodeTimeout) {
					if first {
						return firstDeleteErr
					}
					return errors.Join(firstDeleteErr, err)
				}
			} else {
				deleting[ref.Path] = true
			}
		}
		allAbsent := true
		for _, ref := range bindings {
			found, err := get(ctx, ref.Path)
			if err != nil {
				return errors.Join(firstDeleteErr, err)
			}
			if found.UID != "" {
				allAbsent = false
			}
		}
		if allAbsent {
			return nil
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(firstDeleteErr, e.Wrap(e.CodeTimeout, ctx.Err(), "waiting for terminal cleanup"))
		case <-timer.C:
		}
	}
}
