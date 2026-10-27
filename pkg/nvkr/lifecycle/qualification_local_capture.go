// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"errors"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/factory"
)

// localQualificationCapture retains all payloads matched to the replayed Run.
// It is transport ownership only. In particular, retained disk bytes have not
// yet acquired authenticated material provenance or local signing authority.
type localQualificationCapture struct {
	ordinary      *delivery.Publication
	ordinaryRoot  string
	disks         *localQualificationDisks
	materialBytes string
}

func (capture *localQualificationCapture) Close() error {
	if capture == nil {
		return nil
	}
	return errors.Join(capture.ordinary.Close(), capture.disks.Close())
}

type localOrdinaryCapture func(context.Context, *factory.Plan, string) (*delivery.Publication, string, error)

func captureLocalQualificationPayloads(ctx context.Context, workspace string, inputs *localQualificationInputs) (*localQualificationCapture, error) {
	return captureLocalQualificationPayloadsWith(ctx, workspace, inputs, factory.CaptureWorkspaceOrdinaryPublication)
}

func captureLocalQualificationPayloadsWith(ctx context.Context, workspace string, inputs *localQualificationInputs, ordinary localOrdinaryCapture) (capture *localQualificationCapture, err error) {
	// This checks original slot and outer-publication bindings and captures disks.
	// Factory ordinary assembly verifies producer/package bytes before the held
	// output comparison below. A subsequent failure must close the entire set.
	disks, err := captureLocalQualificationDisks(ctx, workspace, inputs)
	if err != nil {
		return nil, err
	}
	capture = &localQualificationCapture{disks: disks}
	defer func() {
		if err == nil && ctx.Err() != nil {
			err = e.Wrap(e.CodeTimeout, ctx.Err(), "local payload capture canceled")
		}
		if err != nil {
			err = errors.Join(err, capture.Close())
			capture = nil
		}
	}()
	capture.materialBytes, err = loadLocalQualificationMaterials(ctx, workspace, inputs)
	if err != nil {
		return capture, err
	}
	var wanted []Artifact
	for _, output := range inputs.run.Observed {
		if output.Kind != OutputDiskImage {
			wanted = append(wanted, output)
		}
	}
	if len(wanted) != 0 {
		capture.ordinary, capture.ordinaryRoot, err = ordinary(ctx, inputs.plan, workspace)
		if err != nil {
			return capture, err
		}
		if capture.ordinary == nil || !canonical.IsDigest(capture.ordinaryRoot) {
			return capture, e.New(e.CodeGateUnmet, "ordinary capture returned no exact transport owner")
		}
		got, readErr := capture.ordinary.Outputs()
		if readErr != nil {
			return capture, readErr
		}
		// Delivery enriches these descriptions by inspecting the exact held
		// bytes. Ordinary local Run observations do not store those fields;
		// compare their recorded projection while retaining every subject and
		// image-index binding. The held publication keeps its enriched values.
		for i := range got {
			got[i].OS, got[i].Arch, got[i].ChartVersion = "", "", ""
		}
		wantBytes, marshalErr := canonical.Marshal(wanted)
		if marshalErr != nil {
			return capture, marshalErr
		}
		gotBytes, marshalErr := canonical.Marshal(got)
		if marshalErr != nil {
			return capture, marshalErr
		}
		if !bytes.Equal(wantBytes, gotBytes) {
			return capture, e.New(e.CodeVerifyFailed, "held ordinary outputs differ from the original complete Run")
		}
	}
	if len(wanted)+len(disks.members) != len(inputs.run.Expected) {
		return capture, e.New(e.CodeReleaseIncomplete, "held payload set does not cover every named Run output")
	}
	return capture, nil
}
