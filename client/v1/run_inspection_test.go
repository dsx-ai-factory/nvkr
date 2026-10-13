// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package client_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	client "github.com/dsx-ai-factory/nvkr/client/v1"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestSDKRunInspectionRejectsUnavailableAndUnreadyInputs(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, test := range []struct {
		name string
		sdk  *client.Client
		ctx  context.Context
		code e.Code
	}{
		{"missing record", client.New(), t.Context(), e.CodeVerifyFailed},
		{"canceled context", client.New(), canceled, e.CodeTimeout},
		{"nil client", nil, t.Context(), e.CodeUsage},
		{"invalid client configuration", client.New(nil), t.Context(), e.CodeUsage},
		{"nil context", client.New(), nil, e.CodeTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			view, err := test.sdk.InspectRun(test.ctx, filepath.Join(root, "lifecycle.json"))
			var coded *e.Error
			if !errors.As(err, &coded) || coded.Code != test.code || view.RecordAvailable || view.Interrupted || view.Attempt != "" {
				t.Fatalf("unavailable or unready input invented a description: %+v %v", view, err)
			}
		})
	}
}

func TestSDKRunInspectionValidAndRejectedRecords(t *testing.T) {
	repo, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	sdk := client.New()
	prepared, err := sdk.PrepareLifecycleRun(t.Context(), "definitions/nvke-v1/definition.yaml", "definitions/nvke-v1/environment-local.yaml", "test", "sdk-inspection")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		lane string
		code e.Code
	}{
		{"valid frozen record", "", ""},
		{"unsupported qualified lane", "qualified", e.CodeVerifyFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			run := prepared.Description()
			if test.lane != "" {
				run.Lane = test.lane
			}
			root := filepath.Join(parent, run.Attempt)
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			raw, err := canonical.Marshal(run)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "lifecycle.json")
			if err := os.WriteFile(path, raw, 0o400); err != nil {
				t.Fatal(err)
			}
			view, err := sdk.InspectRun(t.Context(), path)
			if test.code != "" {
				var coded *e.Error
				if !errors.As(err, &coded) || coded.Code != test.code {
					t.Fatal("record refusal lost its owning code", err)
				}
				return
			}
			if err != nil || !view.RecordAvailable || view.Interrupted || view.Status != run.Status || view.Attempt != run.Attempt || len(view.Outputs) != len(run.Expected) {
				t.Fatalf("SDK record differs from owning metadata: %+v %v", view, err)
			}
			outputs := append([]client.InspectedRunOutput(nil), view.Outputs...)
			if len(outputs) == 0 {
				t.Fatal("public output alias lost recorded membership")
			}
			if _, present := reflect.TypeFor[client.RunInspection]().MethodByName("Snapshot"); present {
				t.Fatal("SDK metadata exposes presenter projection")
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(raw) {
				t.Fatal("SDK inspection changed the record", err)
			}
		})
	}
}
