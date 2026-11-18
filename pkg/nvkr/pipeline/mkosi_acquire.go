// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/ir"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/mkosi"
	"golang.org/x/sync/errgroup"
)

const mkosiInputDirectory = ".nvkr-package-inputs"
const mkosiAcquisitionTimeout = 20 * time.Minute

func acquireMkosiInputs(ctx context.Context, spec *mkosi.Spec, source string, log io.Writer) error {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: mkosiAcquisitionTimeout, CheckRedirect: func(request *http.Request, prior []*http.Request) error {
		if len(prior) >= 5 || request.URL.Scheme != "https" || request.URL.User != nil {
			return e.New(e.CodeFetchFailed, "package download redirect leaves its admitted HTTPS route")
		}
		return nil
	}}
	return acquireMkosiInputsWithClient(ctx, spec, source, client, log)
}

func acquireMkosiInputsWithClient(ctx context.Context, spec *mkosi.Spec, source string, client *http.Client, log io.Writer) (returnErr error) {
	if ctx == nil || client == nil {
		return e.New(e.CodeUsage, "package acquisition requires context and a bounded client")
	}
	if _, err := ir.MkosiSourceIdentity(spec); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, mkosiAcquisitionTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "package acquisition canceled")
	}
	root := filepath.Join(source, mkosiInputDirectory)
	if err := os.Mkdir(root, 0o700); err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "reserving fresh package input directory")
	}
	metrics := newMkosiAcquisitionMetrics(log)
	started := time.Now()
	defer func() { metrics.report(time.Since(started), returnErr == nil) }()
	group, workCtx := errgroup.WithContext(ctx)
	group.SetLimit(4)
	schedule := func(uri, relative, digest string, size int64) {
		group.Go(func() (requestErr error) {
			started := time.Now()
			body := &mkosiDownloadCounter{}
			defer func() {
				metrics.record(mkosiInputTiming{path: relative, bytes: body.bytes, elapsed: time.Since(started), succeeded: requestErr == nil})
			}()
			// Every request shares the acquisition's remaining twenty-minute
			// budget; slow files no longer have a separate three-minute cutoff.
			requestCtx := workCtx
			response, err := requestMkosiInput(requestCtx, client, uri, func(status, attempt int) {
				metrics.retry(relative, status, attempt)
			})
			if err != nil {
				if requestCtx.Err() != nil {
					return e.Wrap(e.CodeTimeout, requestCtx.Err(), "pinned package acquisition canceled")
				}
				// Redirect URLs can contain server-issued query credentials. Do not
				// reproduce transport errors, response bodies or redirect locations.
				return e.New(e.CodeFetchFailed, "requesting pinned package input")
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK || response.ContentLength > size {
				return e.New(e.CodeFetchFailed, "package input response has unexpected status or size")
			}
			body.reader = response.Body
			_, gotSize, err := writeVerifiedDownload(requestCtx, body, "mkosi input", root, relative, digest, size)
			if err != nil {
				return err
			}
			if gotSize != size {
				return e.New(e.CodeVerifyFailed, "package input size differs from its pin")
			}
			if err := os.Chmod(filepath.Join(root, relative), 0o444); err != nil {
				return e.Wrap(e.CodeFetchFailed, err, "protecting retained package input")
			}
			return nil
		})
	}
	for _, assembly := range spec.Roots {
		for _, repository := range assembly.Inputs.Repositories {
			if workCtx.Err() != nil {
				break
			}
			schedule(repository.URI, assembly.Name+"/metadata/"+strings.TrimPrefix(repository.Digest, "sha256:"), repository.Digest, repository.Size)
		}
		for _, item := range assembly.Inputs.Packages {
			if workCtx.Err() != nil {
				break
			}
			schedule(item.URI, assembly.Name+"/packages/"+strings.TrimPrefix(item.Digest, "sha256:")+".deb", item.Digest, item.Size)
		}
	}
	if err := group.Wait(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "package acquisition canceled before recording inputs")
	}
	for _, assembly := range spec.Roots {
		raw, err := assembly.Inputs.Marshal()
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root, assembly.Name, "inputs.json"), raw, 0o444); err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "recording retained package inputs")
		}
	}
	return nil
}

// Retry only transient HTTP responses, before creating a destination. All
// attempts share the request deadline and still require the exact size/digest.
// Integrity failures and transport errors are never retried or hidden.
func requestMkosiInput(ctx context.Context, client *http.Client, uri string, retryLog func(status, attempt int)) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
		if err != nil {
			return nil, e.New(e.CodeFetchFailed, "constructing pinned package request")
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		retry := response.StatusCode == http.StatusTooManyRequests || response.StatusCode == http.StatusBadGateway ||
			response.StatusCode == http.StatusServiceUnavailable || response.StatusCode == http.StatusGatewayTimeout
		if !retry || attempt == 2 {
			return response, nil
		}
		_ = response.Body.Close()
		if retryLog != nil {
			retryLog(response.StatusCode, attempt+2)
		}
		timer := time.NewTimer(time.Duration(attempt+1) * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
