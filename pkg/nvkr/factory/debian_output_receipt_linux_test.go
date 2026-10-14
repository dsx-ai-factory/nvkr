// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package factory

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestDebianOutputReceiptNativeOriginalJoin(t *testing.T) {
	if os.Getenv("NVKR_TEST_DEBIAN_CONTENT_RUNTIME") != "1" {
		t.Skip("explicit native receipt contract requires real composition capacity")
	}
	for _, mode := range []string{"issued", "decoded Run", "sibling", "closed inspector", "closed Run", "writer failure", "short writer", "invalid writer", "canceled writer", "closed archive", "failed claim retry"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
			defer cancel()
			plan, result, inspector, _ := debianRunContentFixture(t, ctx, "issued")
			if mode == "failed claim retry" {
				if receipt, err := result.debian.observeBoundOutput(ctx, result, plan, plan.LifecycleOutputs[0]); receipt != nil || factoryErrorCode(err) != e.CodeProvenanceMissing {
					t.Fatal("missing inspection issued terminal receipt", err)
				}
				if len(result.debian.receiptClaims) != 0 {
					t.Fatal("failed observation leaked claim")
				}
			}
			if _, err := result.debian.inspectBoundProducer(ctx, result, plan, plan.LifecycleOutputs[0], inspector); err != nil {
				t.Fatal(err)
			}
			original := result
			output := plan.LifecycleOutputs[0]
			if mode == "decoded Run" {
				raw, err := canonical.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				var decoded LocalRunResult
				if err := canonical.UnmarshalExact(raw, &decoded); err != nil {
					t.Fatal(err)
				}
				original = &decoded
			}
			if mode == "sibling" {
				output.Name = "sibling"
			}
			if mode == "closed inspector" {
				if err := inspector.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "closed archive" {
				completion := result.debian.completions[output.ArtifactID]
				if err := completion.archive.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "closed Run" {
				if err := result.Close(); err != nil {
					t.Fatal(err)
				}
			}
			receipt, err := result.debian.observeBoundOutput(ctx, original, plan, output)
			if mode == "decoded Run" || mode == "sibling" {
				if receipt != nil || factoryErrorCode(err) != e.CodeVerifyFailed {
					t.Fatal(err)
				}
				return
			}
			if mode == "closed inspector" || mode == "closed Run" || mode == "closed archive" {
				if receipt != nil || factoryErrorCode(err) != e.CodeEngineCapability {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal("native terminal receipt", err)
			}
			if again, err := result.debian.observeBoundOutput(ctx, result, plan, output); again != nil || factoryErrorCode(err) != e.CodeVerifyFailed {
				t.Fatal("duplicate receipt", err)
			}
			raw, err := canonical.Marshal(receipt)
			if err != nil {
				t.Fatal(err)
			}
			var decoded DebianOutputReceipt
			if err := canonical.UnmarshalExact(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if _, err := decoded.DescribeContext(ctx); factoryErrorCode(err) != e.CodeProvenanceMissing {
				t.Fatal("decoded receipt restored authority", err)
			}
			if mode == "writer failure" || mode == "short writer" || mode == "invalid writer" || mode == "canceled writer" {
				callCtx, callCancel := context.WithCancel(ctx)
				defer callCancel()
				var writer io.Writer = debianReceiptErrorWriter{}
				switch mode {
				case "short writer":
					writer = debianReceiptCountWriter{count: 1}
				case "invalid writer":
					writer = debianReceiptCountWriter{count: -1}
				case "canceled writer":
					writer = debianReceiptCancelWriter{cancel: callCancel}
				}
				got, err := receipt.WriteToContext(callCtx, writer)
				want := e.CodeVerifyFailed
				if mode == "canceled writer" {
					want = e.CodeTimeout
					if !errors.Is(err, context.Canceled) || !errors.Is(err, errDebianReceiptWriterFailure) {
						t.Fatal("cancellation lost original cause", err)
					}
				}
				if got != (DebianOutputObservation{}) || factoryErrorCode(err) != want {
					t.Fatal("failed writer returned terminal result", err)
				}
				return
			}
			var final bytes.Buffer
			got, err := receipt.WriteToContext(ctx, &final)
			if err != nil || final.String() != "controlled final archive" {
				t.Fatal("exact final export", err)
			}
			if got.Subject != output.ArtifactID || got.BuildDigest != output.BuildDigest || got.Size != int64(final.Len()) || got.InputDigest == "" || got.SBOMDigest == "" || got.Target != *plan.Builds[0].Debian {
				t.Fatal("terminal binding changed", got)
			}
			changed := got
			changed.Target.Distribution = "caller description mutation"
			readback, err := receipt.DescribeContext(ctx)
			if err != nil || readback.Target == changed.Target || readback != got {
				t.Fatal("returned description changed receipt", err)
			}
			alias := *receipt
			if err := result.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := alias.DescribeContext(t.Context()); factoryErrorCode(err) != e.CodeEngineCapability {
				t.Fatal("closed Run left receipt authority", err)
			}
		})
	}
}

type debianReceiptErrorWriter struct{}

func (debianReceiptErrorWriter) Write([]byte) (int, error) {
	return 0, errors.New("controlled writer failure")
}

type debianReceiptBlockedWriter struct{ started, release chan struct{} }

func (w *debianReceiptBlockedWriter) Write(p []byte) (int, error) {
	close(w.started)
	<-w.release
	return len(p), nil
}
func TestDebianOutputReceiptNativeCloseJoinsExport(t *testing.T) {
	if os.Getenv("NVKR_TEST_DEBIAN_CONTENT_RUNTIME") != "1" {
		t.Skip("explicit native receipt contract requires real composition capacity")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	plan, result, inspector, _ := debianRunContentFixture(t, ctx, "issued")
	if _, err := result.debian.inspectBoundProducer(ctx, result, plan, plan.LifecycleOutputs[0], inspector); err != nil {
		t.Fatal(err)
	}
	receipt, err := result.debian.observeBoundOutput(ctx, result, plan, plan.LifecycleOutputs[0])
	if err != nil {
		t.Fatal(err)
	}
	writer := &debianReceiptBlockedWriter{started: make(chan struct{}), release: make(chan struct{})}
	exportDone := make(chan error, 1)
	closeDone := make(chan error, 1)
	exportJoined, closeJoined, released, closeStarted := false, false, false, false
	go func() {
		got, err := receipt.WriteToContext(ctx, writer)
		if got != (DebianOutputObservation{}) {
			err = e.New(e.CodeInternal, "canceled export returned result")
		}
		exportDone <- err
	}()
	defer func() {
		cancel()
		if !released {
			close(writer.release)
		}
		if !exportJoined {
			<-exportDone
		}
		if closeStarted && !closeJoined {
			<-closeDone
		}
	}()
	select {
	case <-writer.started:
	case err := <-exportDone:
		exportJoined = true
		t.Fatal("export failed before writer", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	closeStarted = true
	go func() { closeDone <- result.Close() }()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		result.debian.mu.Lock()
		closing := result.debian.closed
		result.debian.mu.Unlock()
		if closing {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	select {
	case err := <-closeDone:
		closeJoined = true
		t.Fatal("Close returned before writer joined", err)
	default:
	}
	close(writer.release)
	released = true
	exportErr := <-exportDone
	exportJoined = true
	if factoryErrorCode(exportErr) != e.CodeTimeout {
		t.Fatal(exportErr)
	}
	closeErr := <-closeDone
	closeJoined = true
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if _, err := receipt.WriteToContext(t.Context(), io.Discard); factoryErrorCode(err) != e.CodeEngineCapability {
		t.Fatal("revoked receipt exported", err)
	}
}

var errDebianReceiptWriterFailure = errors.New("controlled canceled writer failure")

type debianReceiptCancelWriter struct{ cancel context.CancelFunc }

func (w debianReceiptCancelWriter) Write([]byte) (int, error) {
	w.cancel()
	return 0, errDebianReceiptWriterFailure
}

type debianReceiptCountWriter struct{ count int }

func (w debianReceiptCountWriter) Write([]byte) (int, error) { return w.count, nil }

func TestDebianOutputReceiptNativeInspectorCloseDuringExport(t *testing.T) {
	if os.Getenv("NVKR_TEST_DEBIAN_CONTENT_RUNTIME") != "1" {
		t.Skip("explicit native receipt contract requires real composition capacity")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	plan, result, inspector, _ := debianRunContentFixture(t, ctx, "issued")
	if _, err := result.debian.inspectBoundProducer(ctx, result, plan, plan.LifecycleOutputs[0], inspector); err != nil {
		t.Fatal(err)
	}
	receipt, err := result.debian.observeBoundOutput(ctx, result, plan, plan.LifecycleOutputs[0])
	if err != nil {
		t.Fatal(err)
	}
	writer := &debianReceiptBlockedWriter{started: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	joined, released := false, false
	go func() {
		got, err := receipt.WriteToContext(ctx, writer)
		if got != (DebianOutputObservation{}) {
			err = e.New(e.CodeInternal, "closed inspector allowed terminal result")
		}
		done <- err
	}()
	defer func() {
		cancel()
		if !released {
			close(writer.release)
		}
		if !joined {
			<-done
		}
	}()
	select {
	case <-writer.started:
	case err := <-done:
		joined = true
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := inspector.Close(); err != nil {
		t.Fatal(err)
	}
	close(writer.release)
	released = true
	err = <-done
	joined = true
	if factoryErrorCode(err) != e.CodeEngineCapability {
		t.Fatal("export ignored closed original inspector", err)
	}
	completion := result.debian.completions[plan.LifecycleOutputs[0].ArtifactID]
	if _, err := completion.archive.WriteToContext(ctx, io.Discard); err != nil {
		t.Fatal("borrowed inspector released Run bytes", err)
	}
}

func TestDebianOutputReceiptNativeConcurrentClaim(t *testing.T) {
	if os.Getenv("NVKR_TEST_DEBIAN_CONTENT_RUNTIME") != "1" {
		t.Skip("explicit native receipt contract requires real composition capacity")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	plan, result, inspector, root := debianRunContentFixture(t, ctx, "issued")
	output := plan.LifecycleOutputs[0]
	if _, err := result.debian.inspectBoundProducer(ctx, result, plan, output, inspector); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mode"), []byte("terminal barrier"), 0600); err != nil {
		t.Fatal(err)
	}
	callCtx, callCancel := context.WithCancel(ctx)
	defer callCancel()
	done := make(chan error, 1)
	joined := false
	go func() {
		receipt, err := result.debian.observeBoundOutput(callCtx, result, plan, output)
		if receipt != nil {
			err = e.New(e.CodeInternal, "canceled claim issued receipt")
		}
		done <- err
	}()
	defer func() {
		callCancel()
		_ = os.WriteFile(filepath.Join(root, "release-terminal"), nil, 0600)
		if !joined {
			<-done
		}
	}()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(root, "terminal-started")); err == nil {
			break
		}
		select {
		case err := <-done:
			joined = true
			t.Fatal("claim ended before barrier", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	if receipt, err := result.debian.observeBoundOutput(ctx, result, plan, output); receipt != nil || factoryErrorCode(err) != e.CodeVerifyFailed {
		t.Fatal("competing claim reached terminal work", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "terminal-image-calls"))
	if err != nil || string(raw) != "image\n" {
		t.Fatal("competing observer performed image I/O", string(raw), err)
	}
	callCancel()
	firstErr := <-done
	joined = true
	if factoryErrorCode(firstErr) != e.CodeTimeout || !errors.Is(firstErr, context.Canceled) {
		t.Fatal("canceled claim lost context", firstErr)
	}
	if len(result.debian.receiptClaims) != 0 || len(result.debian.receipts) != 0 {
		t.Fatal("canceled claim left authority")
	}
	if err := os.WriteFile(filepath.Join(root, "mode"), []byte("issued"), 0600); err != nil {
		t.Fatal(err)
	}
	if receipt, err := result.debian.observeBoundOutput(ctx, result, plan, output); receipt == nil || err != nil {
		t.Fatal("released claim prevented fresh observation", err)
	}
}

func TestDebianOutputReceiptNativeFreezesNamedOutput(t *testing.T) {
	if os.Getenv("NVKR_TEST_DEBIAN_CONTENT_RUNTIME") != "1" {
		t.Skip("explicit native receipt contract requires real composition capacity")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	plan, result, inspector, root := debianRunContentFixture(t, ctx, "issued")
	original := plan.LifecycleOutputs[0]
	if _, err := result.debian.inspectBoundProducer(ctx, result, plan, original, inspector); err != nil {
		t.Fatal(err)
	}
	output := original
	output.Components = append([]string(nil), output.Components...)
	if err := os.WriteFile(filepath.Join(root, "mode"), []byte("terminal barrier"), 0600); err != nil {
		t.Fatal(err)
	}
	type completed struct {
		receipt *DebianOutputReceipt
		err     error
	}
	done := make(chan completed, 1)
	joined := false
	go func() {
		receipt, err := result.debian.observeBoundOutput(ctx, result, plan, output)
		done <- completed{receipt, err}
	}()
	defer func() {
		cancel()
		_ = os.WriteFile(filepath.Join(root, "release-terminal"), nil, 0600)
		if !joined {
			<-done
		}
	}()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(filepath.Join(root, "terminal-started")); err == nil {
			break
		}
		select {
		case got := <-done:
			joined = true
			t.Fatal("observation ended before barrier", got.err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	// The runtime marker establishes the phase; the owner mutex also gives
	// the race detector a Go happens-before edge from completed admission.
	result.debian.mu.Lock()
	output.Components[0] = "caller substitution after admission"
	result.debian.mu.Unlock()
	if err := os.WriteFile(filepath.Join(root, "release-terminal"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	got := <-done
	joined = true
	if got.err != nil || got.receipt == nil {
		t.Fatal(got.err)
	}
	observation, err := got.receipt.DescribeContext(ctx)
	if err != nil || observation.Subject != original.ArtifactID || observation.Component != original.Components[0] {
		t.Fatal("caller slice replaced original terminal scope", err)
	}
}
