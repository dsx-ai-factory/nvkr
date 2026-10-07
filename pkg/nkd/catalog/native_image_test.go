// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package catalog

import (
	"context"
	"strings"
	"testing"
	"time"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

func TestNativeImageSelectionOriginalOwnerAndScope(t *testing.T) {
	for _, media := range []types.MediaType{types.OCIManifestSchema1, types.DockerManifestSchema2} {
		for _, arch := range []string{"amd64", "arm64"} {
			t.Run(string(media)+"/"+arch, func(t *testing.T) {
				image := imagePinFixture(t, arch, media)
				child, err := image.Digest()
				if err != nil {
					t.Fatal(err)
				}
				indexMedia := types.OCIImageIndex
				if media == types.DockerManifestSchema2 {
					indexMedia = types.DockerManifestList
				}
				index := pinIndexFixture(indexMedia, mutate.IndexAddendum{Add: image, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: arch}}})
				parent, err := index.Digest()
				if err != nil {
					t.Fatal(err)
				}
				for _, nativeParent := range []bool{false, true} {
					parentDigest := parent.String()
					source := pinImageSource{index: index}
					if nativeParent {
						parentDigest, source = child.String(), pinImageSource{image: image}
					}
					var retained *NativeImageSelection
					consumed := false
					err := withNativeImage(t.Context(), "example.invalid/runtime@"+parentDigest, child.String(), "linux/"+arch,
						func(_ context.Context, reference name.Reference) (pinImageSource, error) {
							if reference.Name() != "example.invalid/runtime@"+parentDigest {
								t.Fatalf("substituted repository/parent %s", reference.Name())
							}
							return source, nil
						}, func(ctx context.Context, selection *NativeImageSelection) error {
							retained = selection
							if selection.core.repository != "example.invalid/runtime" || selection.core.parent != parentDigest || selection.core.child != child.String() || selection.core.arch != arch {
								t.Fatal("incomplete owner binding")
							}
							copied := *selection
							assertCatalogContractCode(t, copied.Consume(ctx, func(context.Context, v1.Image) error { t.Fatal("copied owner consumed"); return nil }), e.CodeEngineCapability)
							if err := selection.Consume(ctx, func(_ context.Context, got v1.Image) error {
								if got != image {
									t.Fatal("consumer did not receive original authenticated image")
								}
								consumed = true
								return nil
							}); err != nil {
								return err
							}
							assertCatalogContractCode(t, selection.Consume(ctx, func(context.Context, v1.Image) error { t.Fatal("replayed selection reached consumer"); return nil }), e.CodeEngineCapability)
							return nil
						})
					if err != nil || !consumed {
						t.Fatalf("original owner: %v consumed=%v", err, consumed)
					}
					assertCatalogContractCode(t, retained.Consume(t.Context(), func(context.Context, v1.Image) error { t.Fatal("expired selection consumed"); return nil }), e.CodeEngineCapability)
				}
			})
		}
	}
	assertCatalogContractCode(t, (&NativeImageSelection{}).Consume(t.Context(), func(context.Context, v1.Image) error { t.Fatal("caller handle consumed"); return nil }), e.CodeEngineCapability)
}

func TestNativeImageMembershipRejectsBeforeConsumer(t *testing.T) {
	amd, arm := imagePinFixture(t, "amd64", types.OCIManifestSchema1), imagePinFixture(t, "arm64", types.OCIManifestSchema1)
	child, err := amd.Digest()
	if err != nil {
		t.Fatal(err)
	}
	add := mutate.IndexAddendum{Add: amd, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: "amd64"}}}
	index := pinIndexFixture(types.OCIImageIndex, add)
	parent, err := index.Digest()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, ref, digest, platform string
		source                      pinImageSource
		code                        e.Code
	}{
		{"wrong parent", "example.invalid/runtime@sha256:" + strings.Repeat("a", 64), child.String(), "linux/amd64", pinImageSource{index: index}, e.CodeVerifyFailed},
		{"wrong native", "example.invalid/runtime@" + parent.String(), "sha256:" + strings.Repeat("a", 64), "linux/amd64", pinImageSource{index: index}, e.CodeVerifyFailed},
		{"wrong architecture", "example.invalid/runtime@" + parent.String(), child.String(), "linux/arm64", pinImageSource{index: index}, e.CodeVerifyFailed},
		{"ambiguous native", "example.invalid/runtime@" + parent.String(), child.String(), "linux/amd64", pinImageSource{index: pinIndexFixture(types.OCIImageIndex, add, add)}, e.CodeVerifyFailed},
		{"lying platform", "example.invalid/runtime@" + parent.String(), child.String(), "linux/amd64", pinImageSource{index: pinIndexFixture(types.OCIImageIndex, mutate.IndexAddendum{Add: arm, Descriptor: add.Descriptor})}, e.CodeVerifyFailed},
		{"config substitution", "example.invalid/runtime@" + child.String(), child.String(), "linux/amd64", pinImageSource{image: pinImageBytes{Image: amd, config: []byte(`{"architecture":"arm64","os":"linux"}`)}}, e.CodeVerifyFailed},
		{"tag not parent", "example.invalid/runtime:v1", child.String(), "linux/amd64", pinImageSource{index: index}, e.CodeUnverifiedRef},
		{"unsupported platform", "example.invalid/runtime@" + parent.String(), child.String(), "linux/s390x", pinImageSource{index: index}, e.CodeUnverifiedRef},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := withNativeImage(t.Context(), tc.ref, tc.digest, tc.platform, func(context.Context, name.Reference) (pinImageSource, error) { return tc.source, nil }, func(context.Context, *NativeImageSelection) error {
				t.Fatal("unauthenticated description reached consumer")
				return nil
			})
			assertCatalogContractCode(t, err, tc.code)
		})
	}
}

func TestNativeImageCallbackExitCancelsAndJoins(t *testing.T) {
	image := imagePinFixture(t, "amd64", types.OCIManifestSchema1)
	digest, err := image.Digest()
	if err != nil {
		t.Fatal(err)
	}
	entered, finished, joined := make(chan struct{}), make(chan struct{}), make(chan struct{})
	err = withNativeImage(t.Context(), "example.invalid/runtime@"+digest.String(), digest.String(), "linux/amd64",
		func(context.Context, name.Reference) (pinImageSource, error) {
			return pinImageSource{image: image}, nil
		},
		func(ctx context.Context, selection *NativeImageSelection) error {
			go func() {
				defer close(joined)
				_ = selection.Consume(ctx, func(ctx context.Context, _ v1.Image) error {
					defer close(finished)
					close(entered)
					<-ctx.Done()
					return ctx.Err()
				})
			}()
			<-entered
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	finishedAtReturn := false
	select {
	case <-finished:
		finishedAtReturn = true
	default:
	}
	select {
	case <-joined:
	case <-time.After(5 * time.Second):
		t.Fatal("test-owned consumer goroutine did not exit")
	}
	if !finishedAtReturn {
		t.Fatal("callback returned without joining its canceled consumer")
	}
}

func TestNativeImagePreCanceledDoesNotReadOrConsume(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := withNativeImage(ctx, "example.invalid/runtime@sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("b", 64), "linux/amd64",
		func(context.Context, name.Reference) (pinImageSource, error) {
			t.Fatal("canceled acquisition read")
			return pinImageSource{}, nil
		},
		func(context.Context, *NativeImageSelection) error {
			t.Fatal("canceled acquisition consumed")
			return nil
		})
	assertCatalogContractCode(t, err, e.CodeTimeout)
}
