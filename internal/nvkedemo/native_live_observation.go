// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
)

type nativeLiveObservationKind uint8

const (
	nativeLiveBootstrap nativeLiveObservationKind = iota + 1
	nativeLiveInstall
)

// This presentation runs in the existing owned stderr drain, never in an
// additional reader goroutine. The capability may only enqueue nonblocking
// observations. It cannot turn a source log into readiness or joined success.
type nativeLiveObservation struct {
	ctx      context.Context
	kind     nativeLiveObservationKind
	observer nativeBootstrapObserver
}

func newNativeLiveObservation(ctx context.Context, kind nativeLiveObservationKind, progress io.Writer) *nativeLiveObservation {
	observer, ok := progress.(nativeBootstrapObserver)
	if ctx == nil || ctx.Err() != nil || !ok || kind != nativeLiveBootstrap && kind != nativeLiveInstall {
		return nil
	}
	return &nativeLiveObservation{ctx: ctx, kind: kind, observer: observer}
}

func (observation *nativeLiveObservation) drain(ctx context.Context, destination io.Writer, source io.Reader) error {
	// Use the same standard-library line framing as qualificationprogress.
	// Tee raw bytes into the unchanged private sink, including rejected frames.
	reader := bufio.NewReaderSize(io.TeeReader(source, destination), bufio.MaxScanTokenSize)
	parsedBytes, emitted := 0, 0
	oversized := false
	for {
		line, err := reader.ReadSlice('\n')
		parsedBytes += len(line)
		if err != nil && !errors.Is(err, bufio.ErrBufferFull) && !errors.Is(err, io.EOF) {
			return err // Preserve the existing pipe/private-storage failure.
		}
		if ctx.Err() != nil || parsedBytes > diagnosticLimit || emitted == 64 {
			// Observation loss must not stop private capture or owned pipe drain.
			_, drainErr := io.Copy(io.Discard, reader)
			return drainErr
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			oversized = true
			continue
		}
		if errors.Is(err, io.EOF) {
			return nil // An incomplete final record stays private.
		}
		if oversized {
			oversized = false
			continue
		}
		var text string
		switch observation.kind {
		case nativeLiveBootstrap:
			text = nativeBootstrapLogSummary(line)
		case nativeLiveInstall:
			text = nativeInstallItemSummary(line)
		}
		if text != "" && ctx.Err() == nil {
			emitted++
			observation.observer.observeNativeBootstrap("Live partial source observation (not readiness acceptance):\n" + text)
		}
	}
}

func runNativeSiteInstall(ctx context.Context, runner Runner, args []string) error {
	runner.output, runner.privateCaptureStdout = nil, true
	runner.nativeObservations = nativeLiveInstall
	_, err := runner.Capture(ctx, args, "host-nvke-deploy", 0)
	return err
}

// Exact ca3 deploy/nvke-deploy.sh logs each item before invoking its source
// owner. Match only the source deploy/nico inventory, never paths, argv, errors,
// release descriptions or an item supplied by a caller.
func nativeInstallItemSummary(line []byte) string {
	line = bytes.TrimSuffix(line, []byte{'\n'})
	line = bytes.TrimSuffix(line, []byte{'\r'})
	for _, item := range []struct{ name, kind string }{
		{"nvke-base-config", "template"},
		{"spire-tpm-pubhashes", "manifest"},
		{"spire-server-nodeport", "manifest"},
		{"spire-oidc-provider-nodeport", "manifest"},
		{"capnico", "manifest"},
		{"nktp", "helm"},
		{"postgresql", "helm"},
		{"spire-crds", "helm"},
		{"temporal", "helm"},
		{"spire", "helm"},
		{"controller", "helm"},
	} {
		if string(line) == "=== item "+item.name+" ("+item.kind+")" {
			return "Site install item observation: " + item.name + " (" + item.kind + ")\n"
		}
	}
	return ""
}
