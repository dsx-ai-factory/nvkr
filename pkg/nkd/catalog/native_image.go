package catalog

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/target"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
)

// NativeImageSelection is an original-owner, callback-scoped, single-use handle.
// Descriptive lock/IR pins never construct it. Its actual image was authenticated
// against the exact requested repository, parent, child and native platform.
type NativeImageSelection struct {
	core *nativeImageSelectionCore
}

type nativeImageSelectionCore struct {
	owner      *NativeImageSelection
	mu         sync.Mutex // callback exit joins any in-flight synchronous consumption
	ctx        context.Context
	cancel     context.CancelFunc
	image      v1.Image
	repository string
	parent     string
	child      string
	arch       string
	active     atomic.Bool
	used       atomic.Bool
}

// WithNativeImage authenticates parent-to-native membership before the callback
// may perform output work. It makes an acquisition claim, not an upstream build,
// signature or release-approval claim. The issued handle expires on callback exit.
// The acquisition consumer must stage, verify the complete closure and atomically
// commit it; this owner exposes no pathname-writing or freely reusable getter API.
func WithNativeImage(ctx context.Context, parentRef, child, platform string, use func(context.Context, *NativeImageSelection) error) error {
	return withNativeImage(ctx, parentRef, child, platform, readPinImage, use)
}

func withNativeImage(ctx context.Context, parentRef, child, platform string, read pinImageReader, use func(context.Context, *NativeImageSelection) error) error {
	if ctx == nil || read == nil || use == nil {
		return e.New(e.CodeUsage, "native image acquisition requires context, reader and consumer")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "native image acquisition canceled")
	}
	arch := target.Arch(platform)
	if platform != "linux/"+arch || !target.ValidArch(arch) || !canonical.IsDigest(child) {
		return e.New(e.CodeUnverifiedRef, "native image acquisition requires an exact child and supported native platform")
	}
	parsed, err := name.ParseReference(strings.TrimPrefix(parentRef, "oci://"))
	if err != nil {
		return e.Wrap(e.CodeUnverifiedRef, err, "native image acquisition parent reference")
	}
	parent, ok := parsed.(name.Digest)
	if !ok || !canonical.IsDigest(parent.DigestStr()) {
		return e.New(e.CodeUnverifiedRef, "native image acquisition requires an exact parent reference")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var observed pinImageSelection
	pins, err := selectPinImageDigestsAndParent(ctx, parent.String(), []string{arch}, read, &observed)
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "native image membership authentication canceled")
	}
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "authenticate native image parent membership")
	}
	if observed.parent.String() != parent.DigestStr() || pins[arch] != child || observed.images[arch] == nil {
		return e.New(e.CodeVerifyFailed, "native image parent does not bind the expected child/platform")
	}
	if observed.transport != nil {
		if err := observed.transport.admitImageLayers(observed.images[arch]); err != nil {
			return e.Wrap(e.CodeVerifyFailed, err, "authenticate selected native layers")
		}
	}
	receipt := &NativeImageSelection{core: &nativeImageSelectionCore{ctx: ctx, cancel: cancel, image: observed.images[arch],
		repository: parent.Context().Name(), parent: parent.DigestStr(), child: child, arch: arch},
	}
	receipt.core.owner = receipt
	receipt.core.active.Store(true)
	defer func() {
		receipt.core.active.Store(false)
		cancel() // cancel blocked remote reads before joining an in-flight consumer
		receipt.core.mu.Lock()
		defer receipt.core.mu.Unlock()
	}()
	err = use(ctx, receipt)
	if ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "native image acquisition canceled")
	}
	return err
}

// Consume passes the original authenticated image to one synchronous acquisition
// consumer. No caller image, digest or path can replace the owner's selected bytes.
// The consumer executes inside the bounded original remote context lifetime.
func (selection *NativeImageSelection) Consume(ctx context.Context, use func(context.Context, v1.Image) error) error {
	if ctx == nil || selection == nil || selection.core == nil || selection.core.owner != selection || use == nil {
		return e.New(e.CodeEngineCapability, "native image selection is not an active original-owner handle")
	}
	core := selection.core
	if !core.active.Load() || core.image == nil || core.cancel == nil || core.repository == "" ||
		!canonical.IsDigest(core.parent) || !canonical.IsDigest(core.child) || !target.ValidArch(core.arch) {
		return e.New(e.CodeEngineCapability, "native image selection is not an active original-owner handle")
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "native image consumption canceled")
	}
	stop := context.AfterFunc(ctx, core.cancel)
	defer stop()
	if err := core.ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "native image selection expired")
	}
	if !core.used.CompareAndSwap(false, true) {
		return e.New(e.CodeEngineCapability, "native image selection was already consumed")
	}
	core.mu.Lock()
	defer core.mu.Unlock()
	if !core.active.Load() {
		return e.New(e.CodeEngineCapability, "native image selection expired before consumption")
	}
	digest, _, _, err := authenticatePinImage(core.image, core.arch)
	if core.ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, core.ctx.Err(), "native image consumption canceled")
	}
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "native image changed after membership authentication")
	}
	if digest.String() != core.child {
		return e.New(e.CodeVerifyFailed, "native image changed after membership authentication")
	}
	if err := core.ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "native image acquisition canceled")
	}
	return use(core.ctx, core.image)
}
