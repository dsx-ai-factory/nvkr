// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/qualification"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/releasemanifest"
	"github.com/urfave/cli/v2"
)

type qualificationRunPublisher func(context.Context, string, lifecycle.QualificationRuntimeOptions) (*qualification.Publication, error)

type qualificationManifestPublisher func(context.Context, string, lifecycle.QualificationRuntimeOptions) (*releasemanifest.RunResult, error)

func cmdRunQualify() *cli.Command {
	return cmdRunQualifyWithPublisher(qualification.PublishTriggeredRun)
}

func cmdRunQualifyWithPublisher(publish qualificationRunPublisher) *cli.Command {
	return cmdRunQualifyWithPublishers(publish, releasemanifest.PublishTriggeredRun)
}

func cmdRunQualifyWithPublishers(publish qualificationRunPublisher, issue qualificationManifestPublisher) *cli.Command {
	return cmdRunQualifyWithClock(publish, issue, time.Now)
}

func cmdRunQualifyWithClock(publish qualificationRunPublisher, issue qualificationManifestPublisher, now func() time.Time) *cli.Command {
	return &cli.Command{Name: "qualify", Usage: "qualify the authenticated triggering Run under independently selected policy", Flags: []cli.Flag{
		&cli.StringFlag{Name: "policy", Usage: "independently selected qualification policy YAML"},
		&cli.StringFlag{Name: "workspace", Value: ".nvkr/qualification", Usage: "private qualification workspace"},
		&cli.BoolFlag{Name: "issue-manifest", Usage: "authenticate complete production qualification and attest its release manifest (no release tag)"},
		&cli.StringFlag{Name: "out", Usage: "with --issue-manifest, also write this attempt's unsigned qualification outcome here, whatever the result; its inputs are checked first, and a record that cannot be written after qualification never changes the exit status"},
	}, Action: func(c *cli.Context) error {
		if c.Args().Present() || strings.TrimSpace(c.String("policy")) == "" || strings.TrimSpace(c.String("workspace")) == "" {
			return e.New(e.CodeUsage, "qualify requires --policy, a non-empty workspace, and no positional arguments")
		}
		out := c.String("out")
		if c.IsSet("out") && (strings.TrimSpace(out) == "" || !c.Bool("issue-manifest")) {
			return e.New(e.CodeUsage, "qualify --out requires a non-empty path and --issue-manifest")
		}
		started := now()
		var outcomeInputs *releasemanifest.OutcomeInputs
		if out != "" {
			inputs, err := admitQualificationOutcome(c.Context, out, started)
			if err != nil {
				return err
			}
			outcomeInputs = inputs
		}
		var result any
		var manifestResult *releasemanifest.RunResult
		var publishErr error
		if c.Bool("issue-manifest") {
			publication, err := issue(c.Context, c.String("policy"), qualificationRuntimeOptions(c))
			publishErr, manifestResult = err, publication
			if publication != nil {
				result = publication
			}
		} else {
			publication, err := publish(c.Context, c.String("policy"), qualificationRuntimeOptions(c))
			publishErr = err
			if publication != nil {
				result = publication
			}
		}
		// Elapsed time, not a second wall-clock reading, so a clock step cannot
		// put the finish before the start.
		finished := started.Add(max(now().Sub(started), 0))
		var diagnosticsErr error
		if result != nil {
			raw, err := canonical.Marshal(result)
			if err != nil {
				diagnosticsErr = e.Wrap(e.CodeInternal, err, "encoding qualification diagnostics")
			} else if _, err := c.App.Writer.Write(append(raw, '\n')); err != nil {
				diagnosticsErr = e.Wrap(e.CodeInternal, err, "writing qualification diagnostics")
			}
		}
		if outcomeInputs != nil {
			outcomeInputs.FinishedAt = finished
			if err := writeQualificationOutcome(c.Context, out, manifestResult, publishErr, *outcomeInputs); err != nil {
				// The record grants no authority, so it never changes the
				// qualification's exit status; the job's outcome-written output
				// shows that it is missing.
				var coded *e.Error
				code := e.CodeInternal
				if errors.As(err, &coded) {
					code = coded.Code
				}
				fmt.Fprintf(c.App.ErrWriter, "nvkr: %v\n", e.Wrap(code, err, "recording the qualification outcome; the qualification result stands"))
			}
		}
		if diagnosticsErr == nil {
			return publishErr
		}
		return errors.Join(publishErr, diagnosticsErr)
	}}
}

// admitQualificationOutcome checks everything the record needs before
// qualification does any work: the record's inputs, which releasemanifest
// admits from this process's environment, and a destination the command can
// write.
func admitQualificationOutcome(ctx context.Context, path string, at time.Time) (*releasemanifest.OutcomeInputs, error) {
	inputs, err := releasemanifest.AdmitOutcome(ctx, os.Environ(), at)
	if err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); err == nil && info.IsDir() {
		return nil, e.New(e.CodeUsage, "qualify --out names a directory, not a file")
	}
	// 0700 matches the private qualification workspace that shares .nvkr.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "creating the qualification outcome directory")
	}
	probe, err := os.CreateTemp(dir, ".nvkr-*.tmp")
	if err != nil {
		return nil, e.Wrap(e.CodeInternal, err, "the qualification outcome directory is not writable")
	}
	_ = probe.Close()
	_ = os.Remove(probe.Name())
	return inputs, nil
}

// writeQualificationOutcome records the attempt after its diagnostics. A canceled
// context writes nothing: a canceled attempt has no outcome.
func writeQualificationOutcome(ctx context.Context, path string, result *releasemanifest.RunResult, runErr error, inputs releasemanifest.OutcomeInputs) error {
	outcome, err := releasemanifest.NewOutcome(result, runErr, inputs)
	if err != nil {
		return err
	}
	raw, err := outcome.Marshal()
	if err != nil {
		return err
	}
	// Owner-only: the record is redacted, but a caller's directory may be
	// readable by others.
	return writeAtomic(ctx, path, raw, 0o600)
}
