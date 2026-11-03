// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationoutcome

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

// maxEventBytes bounds the runner's workflow_run payload, normally tens of KiB.
const maxEventBytes = 2 << 20

// Repository is an event's repository description.
type Repository struct {
	FullName string
	ID       string
}

// Event describes the producing Run from the runner's workflow_run event. It is
// unauthenticated: ReadEvent admits its shape only.
type Event struct {
	Action         string
	RunID          string
	Attempt        string
	HeadSHA        string
	HeadBranch     string
	Path           string
	Event          string
	Status         string
	Conclusion     string
	RunStartedAt   time.Time
	UpdatedAt      time.Time
	Repository     Repository
	HeadRepository Repository
}

type eventRepository struct {
	FullName string      `json:"full_name"`
	ID       json.Number `json:"id"`
}

type eventDocument struct {
	Action string `json:"action"`
	Run    struct {
		ID             json.Number     `json:"id"`
		Attempt        json.Number     `json:"run_attempt"`
		HeadSHA        string          `json:"head_sha"`
		HeadBranch     string          `json:"head_branch"`
		Path           string          `json:"path"`
		Event          string          `json:"event"`
		Status         string          `json:"status"`
		Conclusion     string          `json:"conclusion"`
		RunStartedAt   string          `json:"run_started_at"`
		UpdatedAt      string          `json:"updated_at"`
		Repository     eventRepository `json:"repository"`
		HeadRepository eventRepository `json:"head_repository"`
	} `json:"workflow_run"`
}

// ReadEvent reads the runner's workflow_run event (GITHUB_EVENT_PATH) within a
// size and time bound. A missing event is CodeProvenanceMissing; an oversize,
// ambiguous or incomplete one is CodeVerifyFailed; cancellation or the time
// bound is CodeTimeout.
func ReadEvent(ctx context.Context, path string) (Event, error) {
	if ctx == nil {
		return Event{}, e.New(e.CodeUsage, "reading the workflow_run event requires context")
	}
	if strings.TrimSpace(path) == "" {
		return Event{}, e.New(e.CodeProvenanceMissing, "no workflow_run event is available")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	held, err := heldfile.OpenContext(ctx, path, heldfile.Options{Label: "workflow_run event", MaxBytes: maxEventBytes})
	if err != nil {
		switch {
		case ctx.Err() != nil:
			return Event{}, e.Wrap(e.CodeTimeout, err, "opening the workflow_run event")
		case errors.Is(err, fs.ErrNotExist):
			return Event{}, e.Wrap(e.CodeProvenanceMissing, err, "opening the workflow_run event")
		default:
			return Event{}, e.Wrap(e.CodeVerifyFailed, err, "opening the workflow_run event")
		}
	}
	defer func() { _ = held.Close() }()
	raw, err := held.ReadAllContext(ctx, maxEventBytes)
	if err != nil {
		if ctx.Err() != nil {
			return Event{}, e.Wrap(e.CodeTimeout, err, "reading the workflow_run event")
		}
		return Event{}, e.Wrap(e.CodeVerifyFailed, err, "reading the workflow_run event")
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return Event{}, e.Wrap(e.CodeVerifyFailed, err, "invalid workflow_run event")
	}
	var document eventDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return Event{}, e.Wrap(e.CodeVerifyFailed, err, "decoding the workflow_run event")
	}
	run := document.Run
	event := Event{Action: document.Action, RunID: string(run.ID), Attempt: string(run.Attempt), HeadSHA: run.HeadSHA, HeadBranch: run.HeadBranch,
		Path: run.Path, Event: run.Event, Status: run.Status, Conclusion: run.Conclusion,
		Repository:     Repository{FullName: run.Repository.FullName, ID: string(run.Repository.ID)},
		HeadRepository: Repository{FullName: run.HeadRepository.FullName, ID: string(run.HeadRepository.ID)}}
	if !numberPattern.MatchString(event.RunID) || !numberPattern.MatchString(event.Attempt) || !commitPattern.MatchString(event.HeadSHA) ||
		!workflowPattern.MatchString(event.Path) || !repositoryPattern.MatchString(event.Repository.FullName) || !numberPattern.MatchString(event.Repository.ID) ||
		!repositoryPattern.MatchString(event.HeadRepository.FullName) || !numberPattern.MatchString(event.HeadRepository.ID) {
		return Event{}, e.New(e.CodeVerifyFailed, "workflow_run event has no exact Run attempt, commit, workflow and repositories")
	}
	for _, field := range []struct {
		value string
		out   *time.Time
	}{{run.RunStartedAt, &event.RunStartedAt}, {run.UpdatedAt, &event.UpdatedAt}} {
		if field.value == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, field.value)
		if err != nil {
			return Event{}, e.Wrap(e.CodeVerifyFailed, err, "workflow_run event has a malformed time")
		}
		*field.out = parsed.UTC()
	}
	return event, nil
}

// QualifierFromEnv identifies the running job's own Run from GitHub Actions'
// default environment. It is a description; nothing here authenticates it.
func QualifierFromEnv(lookup func(string) (string, bool)) (Run, error) {
	if lookup == nil {
		return Run{}, e.New(e.CodeUsage, "qualifier Run lookup is required")
	}
	values := map[string]string{}
	for _, name := range []string{"GITHUB_REPOSITORY", "GITHUB_WORKFLOW_REF", "GITHUB_RUN_ID", "GITHUB_RUN_ATTEMPT", "GITHUB_SHA"} {
		value, ok := lookup(name)
		if !ok || value == "" {
			return Run{}, e.New(e.CodeProvenanceMissing, "qualifier Run identity requires %s from GitHub Actions", name)
		}
		values[name] = value
	}
	// GITHUB_WORKFLOW_REF is <owner>/<repo>/<workflow path>@<ref>.
	workflow, _, found := strings.Cut(strings.TrimPrefix(values["GITHUB_WORKFLOW_REF"], values["GITHUB_REPOSITORY"]+"/"), "@")
	if !found || !strings.HasPrefix(values["GITHUB_WORKFLOW_REF"], values["GITHUB_REPOSITORY"]+"/") {
		return Run{}, e.New(e.CodeVerifyFailed, "qualifier workflow reference is not in the qualifier repository")
	}
	run := Run{Repository: values["GITHUB_REPOSITORY"], Workflow: workflow, RunID: values["GITHUB_RUN_ID"], Attempt: values["GITHUB_RUN_ATTEMPT"], Commit: values["GITHUB_SHA"]}
	if err := run.validate("qualifier"); err != nil {
		return Run{}, err
	}
	return run, nil
}

// FromEvent describes an attempt without per-output checks, taking the source Run
// from the event. Stage producer is a producer Run that did not succeed; stage
// qualify is a qualifier that ended before qualification produced per-output
// checks. Both are errored.
func FromEvent(event Event, qualifier Run, stage Stage, startedAt, finishedAt time.Time) (Outcome, error) {
	if event.Repository != event.HeadRepository {
		return Outcome{}, invalid("has a source Run built from another repository's head")
	}
	outcome := Outcome{SchemaVersion: SchemaVersion, Status: StatusErrored, Stage: stage, Lane: LanePipelineAttested, SourceIdentity: SourceUnauthenticatedEvent,
		Source: Source{Run: Run{Repository: event.Repository.FullName, Workflow: event.Path, RunID: event.RunID, Attempt: event.Attempt, Commit: event.HeadSHA},
			Event: event.Event, Conclusion: event.Conclusion},
		Qualifier: qualifier, StartedAt: FormatTime(startedAt), FinishedAt: FormatTime(finishedAt)}
	if err := outcome.Validate(); err != nil {
		return Outcome{}, err
	}
	return outcome, nil
}
