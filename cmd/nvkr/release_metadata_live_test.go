// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/progress"
	"github.com/urfave/cli/v2"
)

func TestReleaseMetadataLiveKeepsPartialStateLoading(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "closure failure"}[fail], func(t *testing.T) {
			var source runSnapshotSource
			calls := 0
			operation := func(_ context.Context, _ string, options lifecycle.InspectionOptions) ([]byte, error) {
				calls++
				for _, update := range []delivery.MetadataProgress{{RootDigest: "root", Total: 2}, {RootDigest: "root", Done: 1, Total: 2, Name: "tools", Kind: "executable", Output: true}, {RootDigest: "root", Done: 2, Total: 2, Name: "sbom", Kind: "evidence"}} {
					options.Progress(update)
					snapshot := source.Snapshot()
					if snapshot.Status != "loading" || !strings.Contains(snapshot.Note, "closure checks pending") || len(snapshot.Items) != update.Done {
						t.Fatalf("partial state mislabeled: %+v", snapshot)
					}
					for _, item := range snapshot.Items {
						if item.State != "loading" {
							t.Fatal("partial item claimed completion")
						}
					}
				}
				if fail {
					return nil, e.New(e.CodeVerifyFailed, "fixture closure failure")
				}
				return []byte(`{"schemaVersion":"nvkr.dev/release-description/v1","outputs":[],"evidence":[]}`), nil
			}
			services := runPresentationServices{eligible: func(io.Writer) bool { return true }, show: func(ctx context.Context, options runpresenter.Options, s runSnapshotSource, work func(context.Context) error) runpresenter.Result {
				source = s
				if calls != 0 || !options.Live || !options.StayOpen || s.Snapshot().Status != "loading" {
					t.Fatal("UI did not open before metadata load")
				}
				err := work(ctx)
				final := s.Snapshot()
				if fail {
					if final.Status != "failed" || final.FailureCode != string(e.CodeVerifyFailed) {
						t.Fatal("closure failure claimed success")
					}
				} else if final.Status != "complete" {
					t.Fatal("successful owner did not finish")
				}
				return runpresenter.Result{WorkErr: err}
			}}
			var out bytes.Buffer
			app := &cli.App{Writer: &out, Commands: []*cli.Command{{Name: "release", Subcommands: []*cli.Command{releaseExportCmdWithOperation(operation)}}}, Metadata: map[string]interface{}{commandPresentationServicesKey: services}}
			err := app.RunContext(t.Context(), []string{"nvkr", "release", "export", "--reference", "fixture", "--ui=tui"})
			if fail {
				assertLifecycleCommandCode(t, err, e.CodeVerifyFailed)
			} else if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || out.Len() != 0 {
				t.Fatal("live presentation changed owner count or stdout")
			}
		})
	}
}

func TestReleaseMetadataPartialSelectionAndDetachedSnapshots(t *testing.T) {
	source := &releaseMetadataSource{selection: "tools", snapshot: progress.Snapshot{Status: "loading"}}
	source.report(delivery.MetadataProgress{Name: "sibling", Kind: "oci", Output: true, Done: 1, Total: 2})
	if len(source.Snapshot().Items) != 0 {
		t.Fatal("partial view widened selection")
	}
	source.report(delivery.MetadataProgress{Name: "tools", Kind: "executable", Output: true, Done: 2, Total: 2})
	snapshot := source.Snapshot()
	snapshot.Items[0].Fields[0].Value = "mutated"
	if source.Snapshot().Items[0].Fields[0].Value == "mutated" {
		t.Fatal("snapshot mutation reached live state")
	}
}
