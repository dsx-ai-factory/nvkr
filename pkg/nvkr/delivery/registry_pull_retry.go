// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"os"
	"sync"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"golang.org/x/net/http2"
)

const (
	pullReadAttempts = 3
	pullReadTimeout  = 30 * time.Minute
)

// interruptedPullBody is issued only by a checked disk layer's Read. Provider
// error strings, URLs and response details are deliberately not retained.
type interruptedPullBody struct{}

func (*interruptedPullBody) Error() string {
	return "disk layer body interrupted by HTTP/2 peer protocol reset"
}

type pullAcquire func(context.Context, string) (Inspection, error)

func pullWithRecovery(ctx context.Context, out string, acquire pullAcquire) (Inspection, error) {
	return pullWithRecoveryUsing(ctx, out, acquire, os.RemoveAll, waitForPullRead)
}

func pullWithRecoveryUsing(ctx context.Context, out string, acquire pullAcquire,
	remove func(string) error, wait func(context.Context, time.Duration) error,
) (Inspection, error) {
	if _, bounded := ctx.Deadline(); !bounded {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, pullReadTimeout)
		defer cancel()
	}
	for attempt := 1; attempt <= pullReadAttempts; attempt++ {
		if err := deliveryContext(ctx); err != nil {
			return Inspection{}, err
		}
		inspection, retry, err := pullReadAttempt(ctx, out, acquire, remove)
		if err == nil || !retry || attempt == pullReadAttempts {
			return inspection, err
		}
		if err := wait(ctx, time.Duration(attempt)*time.Second); err != nil {
			return Inspection{}, e.Wrap(e.CodeTimeout, err, "waiting to reacquire interrupted publication")
		}
	}
	return Inspection{}, e.New(e.CodeInternal, "publication acquisition attempt bound was exceeded")
}

// Each attempt has one private layout. Cleanup must finish successfully before
// another attempt starts; successful commit and subsequent inspection never
// authorize recovery, even if their errors happen to wrap a transport cause.
func pullReadAttempt(ctx context.Context, out string, acquire pullAcquire, remove func(string) error) (Inspection, bool, error) {
	stage, commit, cleanup, err := stageLayout(out)
	if err != nil {
		return Inspection{}, false, pullStageError(err)
	}
	defer cleanup()
	_, err = acquire(ctx, stage)
	if ctx.Err() != nil {
		err = e.Wrap(e.CodeTimeout, ctx.Err(), "acquiring publication canceled")
	}
	if err != nil {
		if removeErr := remove(stage); removeErr != nil {
			return Inspection{}, false, e.Wrap(e.CodeImmutableViolation, removeErr, "removing failed publication staging")
		}
		return Inspection{}, isInterruptedPullBody(err), err
	}
	if err := commit(); err != nil {
		return Inspection{}, false, pullStageError(err)
	}
	inspection, err := Inspect(ctx, out)
	if err != nil {
		_ = os.RemoveAll(out)
		return Inspection{}, false, err
	}
	return inspection, false, nil
}

func pullStageError(err error) error {
	var coded *e.Error
	if errors.As(err, &coded) {
		return err
	}
	return e.Wrap(e.CodeImmutableViolation, err, "preparing or committing publication staging")
}

func isInterruptedPullBody(err error) bool {
	for depth := 0; depth < 16 && err != nil; depth++ {
		switch err.(type) {
		case *tls.CertificateVerificationError, *os.PathError, *os.LinkError:
			return false
		}
		if _, joined := err.(interface{ Unwrap() []error }); joined {
			return false
		}
		if coded, ok := err.(*e.Error); ok && coded.Code != e.CodeUnavailable {
			return false
		}
		if _, interrupted := err.(*interruptedPullBody); interrupted {
			return true
		}
		wrapped, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = wrapped.Unwrap()
	}
	return false
}

func waitForPullRead(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}

// Recovery is deliberately limited to the observed one-layer disk path. The
// library joins its only layer writer before returning, so a concurrent layer's
// filesystem or integrity failure cannot be hidden by the retriable error.
func writePulledImage(ctx context.Context, stage string, object PublishedObject, image v1.Image) error {
	if object.Output == nil || object.Output.Format != "disk-image" || object.Output.Disk == nil {
		return layout.Path(stage).WriteImage(image)
	}
	layers, err := image.Layers()
	if err != nil {
		return err
	}
	if len(layers) != 1 {
		return layout.Path(stage).WriteImage(image)
	}
	size, err := layers[0].Size()
	if err != nil {
		return err
	}
	if size <= 0 || size != object.Output.Disk.Size {
		return e.New(e.CodeVerifyFailed, "disk layer size differs from publication metadata")
	}
	state := &pullBodyState{}
	layer := &pullObservedLayer{Layer: layers[0], state: state, size: size}
	err = layout.Path(stage).WriteImage(pullObservedImage{Image: image, layer: layer})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	state.mu.Lock()
	closeFailed := state.closeFailed
	state.mu.Unlock()
	if closeFailed {
		// writeBlob's failure path ignores Close errors. They must block recovery.
		return e.New(e.CodeVerifyFailed, "closing publication layer body failed")
	}
	return err
}

type pullObservedImage struct {
	v1.Image
	layer v1.Layer
}

func (image pullObservedImage) Layers() ([]v1.Layer, error) { return []v1.Layer{image.layer}, nil }

type pullBodyState struct {
	mu          sync.Mutex
	closeFailed bool
}

type pullObservedLayer struct {
	v1.Layer
	state *pullBodyState
	size  int64
}

func (layer *pullObservedLayer) Size() (int64, error) { return layer.size, nil }

func (layer *pullObservedLayer) Compressed() (io.ReadCloser, error) {
	body, err := layer.Layer.Compressed()
	if err != nil {
		return nil, err
	}
	return &pullObservedBody{ReadCloser: body, state: layer.state, remaining: layer.size}, nil
}

type pullObservedBody struct {
	io.ReadCloser
	state     *pullBodyState
	remaining int64
}

func (body *pullObservedBody) Read(buffer []byte) (int, error) {
	n, err := body.ReadCloser.Read(buffer)
	if int64(n) >= body.remaining {
		body.remaining = 0
	} else {
		body.remaining -= int64(n)
	}
	if isPeerProtocolReset(err) {
		// The upstream verifier hashes only at EOF. Do not reacquire and hide a
		// possible integrity rejection after all declared bytes were delivered.
		if body.remaining == 0 {
			return n, e.New(e.CodeVerifyFailed, "disk layer reset after its declared size without completing verification")
		}
		return n, &interruptedPullBody{}
	}
	return n, err
}

func isPeerProtocolReset(err error) bool {
	// Only a bounded, single-cause chain may authorize recovery. A joined
	// independent failure or a cyclic/unknown chain must remain nonretryable.
	for depth := 0; depth < 16 && err != nil; depth++ {
		switch err.(type) {
		case *e.Error, *tls.CertificateVerificationError, *os.PathError, *os.LinkError:
			return false
		}
		if _, joined := err.(interface{ Unwrap() []error }); joined {
			return false
		}
		if wrapped, ok := err.(interface{ Unwrap() error }); ok {
			err = wrapped.Unwrap()
			continue
		}
		return peerProtocolResetLeaf(err)
	}
	return false
}

func peerProtocolResetLeaf(err error) bool {
	var stream http2.StreamError
	// Go 1.27's net/http internal StreamError deliberately supports errors.As
	// into this public type. The fixed cause narrows the typed code to a peer
	// RST_STREAM, excluding locally detected malformed headers/trailers. An
	// arbitrary error containing these words can never issue the retry marker.
	return errors.As(err, &stream) && stream.StreamID != 0 && stream.Code == http2.ErrCodeProtocol &&
		stream.Cause != nil && stream.Cause.Error() == "received from peer"
}

func (body *pullObservedBody) Close() error {
	err := body.ReadCloser.Close()
	if err != nil {
		body.state.mu.Lock()
		body.state.closeFailed = true
		body.state.mu.Unlock()
	}
	return err
}
