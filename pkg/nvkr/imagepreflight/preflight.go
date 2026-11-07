// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package imagepreflight observes exact registry metadata availability. Its
// results are point-in-time diagnostics, never execution or release authority.
package imagepreflight

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/target"
	"github.com/google/go-containerregistry/pkg/name"
)

const (
	Anonymous      = "anonymous"
	maxSubjects    = 4096
	workers        = 4
	overallTimeout = 2 * time.Minute
	requestTimeout = 10 * time.Second
)

// Subject is selected by the caller's complete Plan admission. Route names are
// configuration references, never secret values. Placement identifies the
// caller's selected probe placement; it does not authenticate provider allocation
// or claim another runner's network or cached images were observed.
type Subject struct {
	Image     string `json:"image"`
	Platform  string `json:"platform"`
	Route     string `json:"route"`
	Placement string `json:"placement"`
}

type Credential struct{ Username, Token string }
type CredentialResolver func(context.Context, string) (Credential, error)

type Observation struct {
	Subject
	Manifest   string    `json:"manifest"`
	ObservedAt time.Time `json:"observedAt"`
}

type Report struct {
	PlanDigest   string        `json:"planDigest"`
	Scope        string        `json:"scope"`
	Observations []Observation `json:"observations"`
}

// Observe bounds the complete operation, request bodies, workers and retries.
// Every worker is joined before returning, including on cancellation. Failure
// returns no partial report and never includes a registry body or credential.
func Observe(ctx context.Context, planDigest string, subjects []Subject, resolve CredentialResolver) (*Report, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = requestTimeout
	transport.MaxResponseHeaderBytes = 64 << 10
	defer transport.CloseIdleConnections()
	return observe(ctx, planDigest, subjects, resolve, transport, time.Now)
}

func observe(ctx context.Context, planDigest string, subjects []Subject, resolve CredentialResolver, base http.RoundTripper, now func() time.Time) (*Report, error) {
	if ctx == nil || !canonical.IsDigest(planDigest) || len(subjects) > maxSubjects {
		return nil, e.New(e.CodeEngineCapability, "image preflight requires a Plan digest and bounded subjects")
	}
	ctx, cancel := context.WithTimeout(ctx, overallTimeout)
	defer cancel()
	selected := make(map[string]Subject)
	credentials := make(map[string]Credential)
	for _, subject := range subjects {
		pin, err := name.NewDigest(subject.Image)
		if err != nil || pin.Scheme() != "https" || !canonical.IsDigest(pin.DigestStr()) || target.ValidatePlatform(subject.Platform) != nil || subject.Route == "" || subject.Placement == "" {
			return nil, e.New(e.CodeEngineCapability, "image preflight requires exact subjects, platforms, routes and placements")
		}
		subject.Image = pin.Name()
		selected[subjectKey(subject)] = subject
		if subject.Route == Anonymous {
			continue
		}
		if pin.RegistryStr() != "ghcr.io" || resolve == nil {
			return nil, e.New(e.CodeEngineCapability, "image preflight reader requires an admitted GHCR route")
		}
	}
	keys := make([]string, 0, len(selected))
	for key := range selected {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	// Admit the complete selection before a resolver can perform credential I/O.
	for _, key := range keys {
		if ctx.Err() != nil {
			return nil, e.New(e.CodeTimeout, "image preflight canceled or timed out")
		}
		route := selected[key].Route
		if route == Anonymous {
			continue
		}
		if _, exists := credentials[route]; exists {
			continue
		}
		credential, err := resolve(ctx, route)
		if ctx.Err() != nil {
			return nil, e.New(e.CodeTimeout, "image preflight canceled or timed out")
		}
		if err != nil || credential.Username == "" || credential.Token == "" || strings.ContainsAny(credential.Username, ":\r\n") || strings.ContainsAny(credential.Token, "\r\n") {
			return nil, e.New(e.CodeFetchFailed, "image preflight reader credential is absent or malformed")
		}
		credentials[route] = credential
	}
	observations := make([]Observation, len(keys))
	errors := make([]error, len(keys))
	jobs := make(chan int)
	var group sync.WaitGroup
	for range min(workers, len(keys)) {
		group.Go(func() {
			for index := range jobs {
				subject := selected[keys[index]]
				manifest, err := probe(ctx, subject, credentials[subject.Route], base)
				if err != nil {
					errors[index] = err
					continue
				}
				observations[index] = Observation{Subject: subject, Manifest: manifest, ObservedAt: now().UTC()}
			}
		})
	}
dispatch:
	for index := range keys {
		select {
		case jobs <- index:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	group.Wait()
	if ctx.Err() != nil {
		return nil, e.New(e.CodeTimeout, "image preflight canceled or timed out")
	}
	for _, err := range errors {
		if err != nil {
			return nil, err
		}
	}
	return &Report{PlanDigest: planDigest, Scope: "point-in-time manifest/config availability; no layers, runtime verification or release authority", Observations: observations}, nil
}

func subjectKey(subject Subject) string {
	return strings.Join([]string{subject.Image, subject.Platform, subject.Route, subject.Placement}, "\x00")
}
