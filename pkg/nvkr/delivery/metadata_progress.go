// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"

	"golang.org/x/sync/errgroup"
)

const metadataConcurrency = 16

// MetadataProgress describes acquisition, never completeness or verification.
// The first update announces the root with Done=0 and no object. Subsequent
// updates count release members whose bounded controls have arrived or passed cache checks. Configs,
// evidence layers and native image children count with their owning member.
type MetadataProgress struct {
	RootDigest         string
	Done, Total        int
	Name, Kind, Digest string
	Output             bool
}

// MetadataPullOptions retains registry admission separately from telemetry.
// Progress callbacks are serialized and must return promptly. Their values are
// detached descriptions; only the returned inspection has passed closure checks.
type MetadataPullOptions struct {
	Registry RegistryOptions
	Progress func(MetadataProgress)
}

// Join before returning, including on failure: no worker may outlive its stage.
func fetchMetadataObjects(ctx context.Context, objects []PublishedObject, fetch func(context.Context, PublishedObject) error) error {
	group, workers := errgroup.WithContext(ctx)
	group.SetLimit(metadataConcurrency)
	for _, object := range objects {
		if workers.Err() != nil {
			break
		}
		group.Go(func() error {
			if err := deliveryContext(workers); err != nil {
				return err
			}
			return fetch(workers, object)
		})
	}
	if err := group.Wait(); err != nil {
		return err
	}
	return deliveryContext(ctx)
}

func metadataProgressUpdate(rootDigest string, done, total int, object *PublishedObject) MetadataProgress {
	update := MetadataProgress{RootDigest: rootDigest, Done: done, Total: total}
	if object != nil {
		update.Name, update.Digest = object.Name, object.DescriptorDigest
		if object.Output != nil {
			update.Kind = object.Output.Format
			update.Output = true
		} else {
			update.Kind = "evidence"
		}
	}
	return update
}
