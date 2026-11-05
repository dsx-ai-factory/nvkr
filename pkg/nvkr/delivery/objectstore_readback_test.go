// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

func TestObjectReadbackBindsExactStoreObjectAndPin(t *testing.T) {
	store, fixture := newObjectFixture(t, "")
	raw := bytes.Repeat([]byte("x"), 5<<20+1)
	object := objectDescription(raw)
	fixture.mu.Lock()
	fixture.data[object.Key] = raw
	fixture.mu.Unlock()
	stored, receipt, err := store.(*s3ObjectStore).putIfAbsentWithReadback(t.Context(), object, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := receipt.require(t.Context(), store, stored); err != nil {
		t.Fatal(err)
	}
	foreign, _ := newObjectFixture(t, "")
	for _, test := range []struct {
		name    string
		receipt *objectReadback
		store   ObjectStore
		stored  StoredObject
	}{
		{"nil", nil, store, stored}, {"zero", &objectReadback{}, store, stored}, {"foreign store", receipt, foreign, stored},
		{"forged exact fields", &objectReadback{stored: stored}, store, stored},
		{"wrong object", receipt, store, func() StoredObject { x := stored; x.SHA256 = "sha256:" + strings.Repeat("f", 64); return x }()},
		{"wrong version", receipt, store, func() StoredObject { x := stored; x.VersionID = "different"; return x }()},
		{"wrong ETag", receipt, store, func() StoredObject { x := stored; x.ETag = `"different"`; return x }()},
	} {
		t.Run(test.name, func(t *testing.T) {
			requireObjectCode(t, test.receipt.require(t.Context(), test.store, test.stored), e.CodeVerifyFailed)
		})
	}
	unissued := &s3ObjectStore{}
	requireObjectCode(t, (&objectReadback{store: unissued}).require(t.Context(), unissued, StoredObject{}), e.CodeVerifyFailed)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	requireObjectCode(t, receipt.require(t.Context(), store, stored), e.CodeTimeout)
}

type objectCloseErrorAPI struct {
	s3ObjectAPI
	cancel context.CancelFunc
}

func (client objectCloseErrorAPI) GetObject(ctx context.Context, input *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	out, err := client.s3ObjectAPI.GetObject(ctx, input, opts...)
	if err == nil {
		out.Body = objectCloseErrorBody{ReadCloser: out.Body, cancel: client.cancel}
	}
	return out, err
}

type objectCloseErrorBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (body objectCloseErrorBody) Close() error {
	if body.cancel != nil {
		body.cancel()
		return body.ReadCloser.Close()
	}
	return errors.Join(body.ReadCloser.Close(), errors.New("synthetic-secret-close-detail"))
}

func TestObjectReadbackIssuedOnlyAfterCompleteReadAndClose(t *testing.T) {
	for _, scenario := range []string{"fresh acknowledgement", "same-length corruption", "close failure", "close cancels context"} {
		t.Run(scenario, func(t *testing.T) {
			store, fixture := newObjectFixture(t, "")
			raw := bytes.Repeat([]byte("x"), 5<<20+1)
			object := objectDescription(raw)
			if scenario != "fresh acknowledgement" {
				data := bytes.Clone(raw)
				if scenario == "same-length corruption" {
					data[0] ^= 1
				}
				fixture.mu.Lock()
				fixture.data[object.Key] = data
				fixture.mu.Unlock()
			}
			owner := store.(*s3ObjectStore)
			if scenario == "close failure" {
				owner.api = objectCloseErrorAPI{s3ObjectAPI: owner.api}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "close cancels context" {
				owner.api = objectCloseErrorAPI{s3ObjectAPI: owner.api, cancel: cancel}
			}
			_, receipt, err := owner.putIfAbsentWithReadback(ctx, object, bytes.NewReader(raw))
			if receipt != nil {
				t.Fatal("unverified observation issued readback capability")
			}
			switch scenario {
			case "fresh acknowledgement":
				if err != nil {
					t.Fatal(err)
				}
			case "same-length corruption":
				requireObjectCode(t, err, e.CodeImmutableViolation)
			case "close failure":
				requireObjectCode(t, err, e.CodeUnavailable)
			case "close cancels context":
				requireObjectCode(t, err, e.CodeTimeout)
			}
		})
	}
}

func TestObjectStoreAdaptsPartSizeBeforeWriting(t *testing.T) {
	store, fixture := newObjectFixture(t, "")
	owner := store.(*s3ObjectStore)
	for _, size := range []int64{0, 5 << 20, (5 << 20) * 10000, (5<<20)*10000 + 1, 64 << 30, (5 << 30) * 10000} {
		t.Run(strconv.FormatInt(size, 10), func(t *testing.T) {
			object := objectDescription(nil)
			object.Size = size
			if err := owner.validate(object); err != nil {
				t.Fatal(err)
			}
			part := owner.partSize(size)
			if part < owner.options.PartSizeBytes || part > 5<<30 || (size > 0 && (size-1)/part+1 > 10000) {
				t.Fatal("multipart admission exceeds service part limits")
			}
		})
	}
	object := objectDescription(nil)
	object.Size = (5<<30)*10000 + 1
	_, err := owner.PutIfAbsent(t.Context(), object, bytes.NewReader(nil))
	requireObjectCode(t, err, e.CodeUsage)
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.calls) != 0 {
		t.Fatal("oversized object performed provider I/O")
	}
}

// Advertise a >10000-part object but retain only the first adapted part. The
// fixture rejects that request permanently, so no remaining payload is read,
// no inventory or receipt is issued, and only a few MiB cross the local SDK HTTP path.
func TestObjectStoreMultipartUsesAdaptedPartSize(t *testing.T) {
	store, fixture := newObjectFixture(t, "part-fail")
	configured := store.(*s3ObjectStore).options.PartSizeBytes
	size := configured*10000 + 1
	want := configured + 1 // ceil((configured*10000+1)/10000), independent of the production helper.
	source := io.NewSectionReader(bytes.NewReader(make([]byte, want)), 0, size)
	object := objectDescription(nil)
	object.Size = size
	stored, err := store.PutIfAbsent(t.Context(), object, source)
	requireObjectCode(t, err, e.CodeUnavailable)
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.firstPartBytes != want {
		t.Fatalf("first SDK UploadPart body has %d bytes, want adapted %d", fixture.firstPartBytes, want)
	}
	if stored.Key != "" || fixture.calls["initiate"] != 1 || fixture.calls["part"] != 1 || fixture.calls["complete"] != 0 || fixture.calls["abort"] != 1 || fixture.calls["list"] != 1 || len(fixture.uploads) != 0 || !fixture.cleanupLive {
		t.Fatalf("bounded first-part refusal did not clean up exactly its upload: calls=%v remaining=%d", fixture.calls, len(fixture.uploads))
	}
}
