// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package conformance

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/testobservation"
)

const ConfigurationSchema = "nvkr.dev/conformance-invocation/v1"

// FileConfiguration is an operator-selected invocation description, never evidence.
// The private kubeconfig path/context must not be copied into public observations.
type FileConfiguration struct {
	SchemaVersion         string                      `json:"schemaVersion"`
	Hydrophone            Tool                        `json:"hydrophone"`
	Kubectl               Tool                        `json:"kubectl"`
	Kubeconfig            Tool                        `json:"kubeconfig"`
	Context               string                      `json:"context"`
	CNISelector           string                      `json:"cniSelector"`
	Image                 string                      `json:"image"`
	ImageVersion          string                      `json:"imageVersion"`
	BusyboxImage          string                      `json:"busyboxImage"`
	Observation           testobservation.Observation `json:"observation"`
	RunTimeoutSeconds     int64                       `json:"runTimeoutSeconds"`
	CommandTimeoutSeconds int64                       `json:"commandTimeoutSeconds"`
	CleanupTimeoutSeconds int64                       `json:"cleanupTimeoutSeconds"`
}

func LoadOptions(ctx context.Context, path string) (Options, error) {
	if ctx == nil {
		return Options{}, e.New(e.CodeUsage, "conformance requires context")
	}
	if err := ctx.Err(); err != nil {
		return Options{}, e.Wrap(e.CodeTimeout, err, "conformance configuration canceled")
	}
	file, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "private conformance configuration", MaxBytes: 1 << 20})
	if err != nil {
		return Options{}, e.Wrap(e.CodeVerifyFailed, err, "holding conformance configuration")
	}
	defer func() { _ = file.Close() }()
	raw, err := file.ReadAllContext(ctx, 1<<20)
	if err != nil {
		return Options{}, e.Wrap(e.CodeVerifyFailed, err, "reading conformance configuration")
	}
	if err := ctx.Err(); err != nil {
		return Options{}, e.Wrap(e.CodeTimeout, err, "conformance configuration canceled after capture")
	}
	var c FileConfiguration
	if err := canonical.UnmarshalExact(raw, &c); err != nil {
		return Options{}, err
	}
	if c.SchemaVersion != ConfigurationSchema || c.RunTimeoutSeconds <= 0 || c.RunTimeoutSeconds > 43200 || c.CommandTimeoutSeconds <= 0 || c.CommandTimeoutSeconds > 300 || c.CleanupTimeoutSeconds <= 0 || c.CleanupTimeoutSeconds > 600 {
		return Options{}, e.New(e.CodeUsage, "exact conformance configuration schema and bounded timeout seconds required")
	}
	return Options{Hydrophone: c.Hydrophone, Kubectl: c.Kubectl, Kubeconfig: c.Kubeconfig, Context: c.Context, CNISelector: c.CNISelector, Image: c.Image, ImageVersion: c.ImageVersion, BusyboxImage: c.BusyboxImage, Observation: c.Observation, RunTimeout: time.Duration(c.RunTimeoutSeconds) * time.Second, CommandTimeout: time.Duration(c.CommandTimeoutSeconds) * time.Second, CleanupTimeout: time.Duration(c.CleanupTimeoutSeconds) * time.Second}, nil
}

// RunFiles retains a private candidate description beside the original upstream
// results. Review exact public files before the separate evidence emit operation.
func RunFiles(ctx context.Context, path string) (result *Result, runErr error) {
	options, err := LoadOptions(ctx, path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if result == nil {
			return
		}
		// Deferred cleanup retains the private failure record after request cancellation.
		recordCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		runErr = errors.Join(runErr, result.SavePrivateDescription(recordCtx))
	}()
	return Run(ctx, options)
}

// SavePrivateDescription writes no public bundle, signer claim or derived pass.
// Exclusive creation and a held standard-library Root confine these local records.
func (r *Result) SavePrivateDescription(ctx context.Context) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "private conformance record requires context")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "private conformance record canceled")
	}

	if r == nil || r.PrivateWorkspace == "" {
		return failure("private result workspace unavailable")
	}
	root, err := os.OpenRoot(r.PrivateWorkspace)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "holding private result workspace")
	}
	defer func() { _ = root.Close() }()
	before, err := root.Stat(".")
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "observing private workspace")
	}
	raw, err := canonical.Marshal(r.Observation)
	if err != nil {
		return err
	}
	if len(raw) > testobservation.MaxObservationBytes {
		return failure("private candidate observation byte budget")
	}
	members := []struct {
		name  string
		bytes []byte
	}{{"observation-description.json", raw}}
	if len(r.rawJUnit) > 0 {
		members = append(members, struct {
			name  string
			bytes []byte
		}{"review-junit.xml", r.rawJUnit})
	}
	for _, member := range members {
		file, err := root.OpenFile(member.name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "creating private result record")
		}
		var writeErr error
		for remaining := member.bytes; len(remaining) > 0; {
			if writeErr = ctx.Err(); writeErr != nil {
				break
			}
			count := min(len(remaining), 64<<10)
			n, err := file.Write(remaining[:count])
			if err != nil {
				writeErr = err
				break
			}
			if n != count {
				writeErr = io.ErrShortWrite
				break
			}
			remaining = remaining[n:]
		}
		closeErr := errors.Join(file.Sync(), file.Close())
		if err := errors.Join(writeErr, closeErr); err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "retaining private result record")
		}
	}
	after, err := os.Stat(filepath.Clean(r.PrivateWorkspace))
	if err != nil || !os.SameFile(before, after) {
		return failure("private result workspace path changed")
	}
	return nil
}
