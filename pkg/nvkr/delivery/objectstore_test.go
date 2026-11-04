// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type objectFixture struct {
	firstPartBytes   int64
	t                *testing.T
	mu               sync.Mutex
	data             map[string][]byte
	uploads          map[string]map[int][]byte
	calls            map[string]int
	mode             string
	getEcho          string
	missingETag      bool
	deniedVersion    bool
	partStarted      chan struct{}
	partCanceled     chan struct{}
	cleanupLive      bool
	completeConflict []byte
}

func (fixture *objectFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Amz-Expected-Bucket-Owner") != "123456789012" {
		fixture.t.Error("missing expected bucket owner")
	}
	if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
		fixture.t.Error("request was not SDK signed")
	}
	if !strings.HasPrefix(r.URL.Path, "/bucket/delivery/") {
		fixture.t.Error("physical key escaped configured prefix")
	}
	key := strings.TrimPrefix(r.URL.Path, "/bucket/delivery/")
	query := r.URL.Query()
	operation := r.Method
	if query.Has("uploads") {
		operation = "initiate"
	} else if query.Get("uploadId") != "" {
		operation = map[string]string{"PUT": "part", "POST": "complete", "DELETE": "abort", "GET": "list"}[r.Method]
	}
	fixture.mu.Lock()
	fixture.calls[operation]++
	count := fixture.calls[operation]
	fixture.mu.Unlock()

	var raw []byte
	if r.Body != nil {
		reader := io.Reader(r.Body)
		if strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") {
			reader = httputil.NewChunkedReader(r.Body)
		}
		var err error
		raw, err = io.ReadAll(reader)
		if err != nil {
			return
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			return
		}
	}
	if operation == "part" && fixture.partStarted != nil {
		select {
		case fixture.partStarted <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		close(fixture.partCanceled)
		return
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fail := func(status int, code string) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(status)
		_, _ = fmt.Fprintf(w, "<Error><Code>%s</Code><Message>synthetic-secret-provider-text</Message></Error>", code)
	}
	switch operation {
	case "PUT":
		if fixture.mode == "put-retry-conflict" {
			if count == 2 {
				fixture.data[key] = bytes.Clone(fixture.completeConflict)
			}
			fail(500, "InternalError")
			return
		}
		if fixture.mode == "always-fail" {
			fail(500, "InternalError")
			return
		}
		if r.Header.Get("If-None-Match") != "*" {
			fixture.t.Error("unconditional object PUT")
		}
		if _, exists := fixture.data[key]; exists {
			fail(412, "PreconditionFailed")
			return
		}
		fixture.data[key] = bytes.Clone(raw)
		if fixture.mode == "put-lost" && count == 1 {
			fail(500, "InternalError")
			return
		}
		w.Header().Set("ETag", `"stored"`)
		w.Header().Set("X-Amz-Version-Id", "v1")
	case "HEAD":
		data, exists := fixture.data[key]
		if !exists {
			fail(403, "AccessDenied")
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		if !fixture.missingETag {
			w.Header().Set("ETag", `"stored"`)
		}
		if fixture.mode != "unversioned" {
			w.Header().Set("X-Amz-Version-Id", "v1")
		}
	case "GET":
		if fixture.deniedVersion && query.Get("versionId") != "" {
			fail(403, "AccessDenied")
			return
		}
		data, exists := fixture.data[key]
		if !exists {
			fail(403, "AccessDenied")
			return
		}
		if query.Get("versionId") == "" && r.Header.Get("If-Match") != `"stored"` {
			fail(412, "PreconditionFailed")
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		w.Header().Set("ETag", `"stored"`)
		if query.Get("versionId") != "" {
			w.Header().Set("X-Amz-Version-Id", query.Get("versionId"))
		}
		switch fixture.getEcho {
		case "wrong-version":
			w.Header().Set("X-Amz-Version-Id", "different")
		case "missing-version":
			w.Header().Del("X-Amz-Version-Id")
		case "wrong-etag":
			w.Header().Set("ETag", `"different"`)
		}
		_, _ = w.Write(data)
	case "initiate":
		id := fmt.Sprintf("owned-%d", count)
		fixture.uploads[id] = make(map[int][]byte)
		_, _ = fmt.Fprintf(w, "<InitiateMultipartUploadResult><UploadId>%s</UploadId></InitiateMultipartUploadResult>", id)
	case "part":
		if count == 1 {
			fixture.firstPartBytes = int64(len(raw))
		}
		if fixture.mode == "part-fail" {
			fail(403, "AccessDenied")
			return
		}
		part, _ := strconv.Atoi(query.Get("partNumber"))
		fixture.uploads[query.Get("uploadId")][part] = bytes.Clone(raw)
		sum := make([]byte, 4)
		binary.BigEndian.PutUint32(sum, crc32.ChecksumIEEE(raw))
		w.Header().Set("ETag", fmt.Sprintf(`"part-%d"`, part))
		w.Header().Set("X-Amz-Checksum-Crc32", base64.StdEncoding.EncodeToString(sum))
	case "complete":
		if r.Header.Get("If-None-Match") != "*" {
			fixture.t.Error("unconditional multipart completion")
		}
		if _, exists := fixture.data[key]; exists {
			fail(412, "PreconditionFailed")
			return
		}
		if fixture.mode == "embedded-conflict" && count == 1 {
			// A different writer wins before this SDK completion retry.
			fixture.data[key] = bytes.Clone(fixture.completeConflict)
			fail(200, "InternalError")
			return
		}
		if fixture.mode == "complete-409-conflict" && count == 1 {
			fixture.data[key] = bytes.Clone(fixture.completeConflict)
			fail(409, "ConditionalRequestConflict")
			return
		}
		if fixture.mode == "complete-409" && count == 1 {
			fail(409, "ConditionalRequestConflict")
			return
		}
		if fixture.mode == "embedded" && count == 1 {
			fail(200, "InternalError")
			return
		}
		parts, exists := fixture.uploads[query.Get("uploadId")]
		if !exists {
			fail(404, "NoSuchUpload")
			return
		}
		var assembled []byte
		for index := 1; index <= len(parts); index++ {
			assembled = append(assembled, parts[index]...)
		}
		fixture.data[key] = assembled
		delete(fixture.uploads, query.Get("uploadId"))
		if (fixture.mode == "complete-lost-retry" || fixture.mode == "complete-lost-conflict") && count == 1 {
			if fixture.mode == "complete-lost-conflict" {
				// Simulate external replacement after our unknown completion;
				// the provider must reject the conflicting full-byte read.
				fixture.data[key] = bytes.Clone(fixture.completeConflict)
			}
			fail(500, "InternalError")
			return
		}
		if fixture.mode == "complete-lost" {
			fail(404, "NoSuchUpload")
			return
		}
		w.Header().Set("X-Amz-Version-Id", "v1")
		_, _ = io.WriteString(w, `<CompleteMultipartUploadResult><ETag>"stored"</ETag></CompleteMultipartUploadResult>`)
	case "abort":
		fixture.cleanupLive = r.Context().Err() == nil
		if _, exists := fixture.uploads[query.Get("uploadId")]; !exists {
			fail(404, "NoSuchUpload")
			return
		}
		delete(fixture.uploads, query.Get("uploadId"))
	case "list":
		fail(404, "NoSuchUpload")
	default:
		fixture.t.Errorf("unexpected request %s %s", r.Method, r.URL)
		fail(400, "InvalidRequest")
	}
}

func newObjectFixture(t *testing.T, mode string) (ObjectStore, *objectFixture) {
	t.Helper()
	fixture := &objectFixture{t: t, data: make(map[string][]byte), uploads: make(map[string]map[int][]byte), calls: make(map[string]int), mode: mode}
	server := httptest.NewTLSServer(fixture)
	t.Cleanup(server.Close)
	previous := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	options := objectTestOptions()
	options.Endpoint = server.URL
	store, err := NewObjectStore(t.Context(), ObjectStoreOptions{Provider: "s3", S3: options})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store, fixture
}

func objectTestOptions() S3ObjectStoreOptions {
	return S3ObjectStoreOptions{Region: "us-east-1", Bucket: "bucket", Prefix: "delivery", Addressing: "path", ExpectedBucketOwner: "123456789012",
		OperationTimeout: 15 * time.Second, CleanupTimeout: time.Second, PartSizeBytes: 5 << 20, MaxAttempts: 2, MaxUploadAttempts: 2,
		Credentials: &aws.Credentials{AccessKeyID: "synthetic-access", SecretAccessKey: "synthetic-secret", SessionToken: "synthetic-session", CanExpire: true, Expires: time.Now().Add(2 * time.Hour)}}
}
func objectDescription(raw []byte) Object {
	sum := sha256.Sum256(raw)
	return Object{Key: "blob", SHA256: "sha256:" + hex.EncodeToString(sum[:]), Size: int64(len(raw)), MediaType: "application/octet-stream"}
}
func requireObjectCode(t *testing.T, err error, want e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != want {
		t.Fatalf("error=%v want code=%v", err, want)
	}
}

func TestObjectStoreSDKTransfersExactBytes(t *testing.T) {
	for _, test := range []struct {
		name, mode   string
		size         int
		wantInitiate int
	}{
		{"empty", "", 0, 0}, {"ordinary bytes", "", 321, 0}, {"lost PUT acknowledgement", "put-lost", 321, 0},
		{"multipart", "", 5<<20 + 731, 1}, {"embedded completion error", "embedded", 5<<20 + 731, 1},
		{"lost completed upload", "complete-lost", 5<<20 + 731, 1}, {"409 fresh upload", "complete-409", 5<<20 + 731, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, fixture := newObjectFixture(t, test.mode)
			raw := bytes.Repeat([]byte{0x37}, test.size)
			object := objectDescription(raw)
			stored, err := store.PutIfAbsent(t.Context(), object, bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			fixture.mu.Lock()
			got := bytes.Clone(fixture.data[object.Key])
			initiated := fixture.calls["initiate"]
			fixture.mu.Unlock()
			if !bytes.Equal(got, raw) || initiated != test.wantInitiate {
				t.Fatal("SDK changed bytes or multipart attempt count")
			}
			if test.mode == "" {
				fixture.mu.Lock()
				gets := fixture.calls["GET"]
				fixture.mu.Unlock()
				if gets != 0 {
					t.Fatal("successful PUT downloaded payload before owner readback")
				}
			}
			reader, err := store.OpenPinned(t.Context(), stored)
			if err != nil {
				t.Fatal(err)
			}
			read, readErr := io.ReadAll(reader)
			closeErr := reader.Close()
			if readErr != nil || closeErr != nil || !bytes.Equal(read, raw) {
				t.Fatalf("readback failed: %v %v", readErr, closeErr)
			}
		})
	}
}

func TestObjectStoreConditionalReuseAndConflict(t *testing.T) {
	for _, same := range []bool{true, false} {
		t.Run(fmt.Sprint(same), func(t *testing.T) {
			store, fixture := newObjectFixture(t, "")
			raw := []byte("new bytes")
			object := objectDescription(raw)
			fixture.mu.Lock()
			fixture.data[object.Key] = []byte("old bytes")
			if same {
				fixture.data[object.Key] = bytes.Clone(raw)
			}
			fixture.mu.Unlock()
			_, err := store.PutIfAbsent(t.Context(), object, bytes.NewReader(raw))
			if same {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				requireObjectCode(t, err, e.CodeImmutableViolation)
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if fixture.calls["PUT"] != 1 {
				t.Fatal("412 incorrectly retried")
			}
			if !same && string(fixture.data[object.Key]) != "old bytes" {
				t.Fatal("existing object was overwritten")
			}
		})
	}
}

func TestObjectStorePinsReadsWithoutDowngrade(t *testing.T) {
	for _, test := range []struct {
		name, etag, version string
		missing, denied     bool
		want                e.Code
	}{
		{name: "exact version", version: "v1"}, {name: "strong ETag", etag: `"stored"`}, {name: "discovered strong ETag"},
		{name: "wildcard", etag: "*", want: e.CodeVerifyFailed}, {name: "weak", etag: `W/"stored"`, want: e.CodeVerifyFailed},
		{name: "list", etag: `"stored", "other"`, want: e.CodeVerifyFailed}, {name: "missing", missing: true, want: e.CodeVerifyFailed},
		{name: "changed", etag: `"changed"`, want: e.CodeUnavailable}, {name: "denied version", version: "v1", denied: true, want: e.CodeUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, fixture := newObjectFixture(t, "unversioned")
			raw := []byte("payload")
			object := objectDescription(raw)
			fixture.mu.Lock()
			fixture.data[object.Key] = raw
			fixture.missingETag = test.missing
			fixture.deniedVersion = test.denied
			fixture.mu.Unlock()
			reader, err := store.OpenPinned(t.Context(), StoredObject{Object: object, VersionID: test.version, ETag: test.etag})
			if test.want != "" {
				requireObjectCode(t, err, test.want)
				if reader != nil {
					t.Fatal("failed read leaked body")
				}
				if strings.Contains(err.Error(), "synthetic-secret") {
					t.Fatal("provider error text leaked")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			if err := reader.Close(); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(data, raw) {
				t.Fatal("wrong bytes")
			}
		})
	}
}

func TestObjectStoreAdmissionAndTemporarySession(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*S3ObjectStoreOptions)
		want   e.Code
	}{
		{"empty identity", func(o *S3ObjectStoreOptions) { o.Credentials = nil }, e.CodeTrustPolicyMissing},
		{"static identity", func(o *S3ObjectStoreOptions) { o.Credentials.CanExpire = false }, e.CodeTrustPolicyMissing},
		{"insufficient lifetime", func(o *S3ObjectStoreOptions) { o.Credentials.Expires = time.Now().Add(time.Second) }, e.CodeUnavailable},
		{"ambiguous", func(o *S3ObjectStoreOptions) {
			o.RoleARN = "arn:aws:iam::123456789012:role/synthetic"
			o.TokenSource = func(context.Context) ([]byte, error) { t.Fatal("unadmitted token I/O"); return nil, nil }
		}, e.CodeTrustPolicyMissing},
		{"HTTP endpoint", func(o *S3ObjectStoreOptions) { o.Endpoint = "http://example.test" }, e.CodeUsage},
		{"empty query", func(o *S3ObjectStoreOptions) { o.Endpoint = "https://example.test?" }, e.CodeUsage},
		{"missing owner", func(o *S3ObjectStoreOptions) { o.ExpectedBucketOwner = "" }, e.CodeUsage},
		{"invalid prefix", func(o *S3ObjectStoreOptions) { o.Prefix = "../escape" }, e.CodeUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			o := objectTestOptions()
			test.change(&o)
			store, err := NewObjectStore(t.Context(), ObjectStoreOptions{Provider: "s3", S3: o})
			requireObjectCode(t, err, test.want)
			if store != nil {
				_ = store.Close()
				t.Fatal("invalid options issued store")
			}
		})
	}
}

func TestObjectStoreCloseJoinsMultipartCleanup(t *testing.T) {
	store, fixture := newObjectFixture(t, "")
	fixture.partStarted = make(chan struct{}, 1)
	fixture.partCanceled = make(chan struct{})
	raw := bytes.Repeat([]byte("x"), 5<<20+1)
	returned := make(chan error, 1)
	go func() {
		_, err := store.PutIfAbsent(t.Context(), objectDescription(raw), bytes.NewReader(raw))
		returned <- err
	}()
	select {
	case <-fixture.partStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("part did not start")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-returned; err == nil {
		t.Fatal("canceled multipart reported success")
	}
	select {
	case <-fixture.partCanceled:
	case <-time.After(5 * time.Second):
		t.Fatal("part HTTP was not canceled")
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.calls["abort"] != 1 || fixture.calls["list"] != 1 || !fixture.cleanupLive || len(fixture.uploads) != 0 {
		t.Fatal("Close failed to join fresh bounded owned cleanup")
	}
}

func TestObjectStoreCloseClosesOutstandingBody(t *testing.T) {
	store, fixture := newObjectFixture(t, "")
	raw := []byte("payload")
	object := objectDescription(raw)
	fixture.mu.Lock()
	fixture.data[object.Key] = raw
	fixture.mu.Unlock()
	reader, err := store.OpenPinned(t.Context(), StoredObject{Object: object, VersionID: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(make([]byte, 1)); err == nil {
		t.Fatal("closed store still served bytes")
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestObjectStoreRefusesOutOfScopeBeforeHTTP(t *testing.T) {
	store, fixture := newObjectFixture(t, "")
	for _, key := range []string{"../other/blob", "/absolute", "delivery/../escape", "line\nfeed"} {
		object := objectDescription([]byte("payload"))
		object.Key = key
		_, err := store.PutIfAbsent(t.Context(), object, strings.NewReader("payload"))
		requireObjectCode(t, err, e.CodeUsage)
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.calls) != 0 {
		t.Fatal("invalid key performed provider I/O")
	}
}

func TestObjectETagProfile(t *testing.T) {
	for _, value := range []string{"*", "W/\"tag\"", "unquoted", `"tag", "second"`, `"bad` + "\r" + `tag"`} {
		if validObjectETag(value) {
			t.Fatalf("accepted %q", value)
		}
	}
	for _, value := range []string{`"tag"`, `"*"`} {
		if !validObjectETag(value) {
			t.Fatalf("refused %q", value)
		}
	}
}

func TestObjectStoreBoundsSDKAndWholePutRetries(t *testing.T) {
	store, fixture := newObjectFixture(t, "always-fail")
	raw := []byte("payload")
	_, err := store.PutIfAbsent(t.Context(), objectDescription(raw), bytes.NewReader(raw))
	requireObjectCode(t, err, e.CodeUnavailable)
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if fixture.calls["PUT"] != 4 || fixture.calls["HEAD"] != 2 || fixture.calls["GET"] != 0 {
		t.Fatalf("unexpected bounded requests: %v", fixture.calls)
	}
}

func TestObjectStoreBoundsProviderDocuments(t *testing.T) {
	for _, test := range []struct {
		name, method, query string
		status              int
		bounded             bool
	}{
		{"error GET", http.MethodGet, "", 403, true}, {"list parts success", http.MethodGet, "?uploadId=owned", 200, true},
		{"completion success", http.MethodPost, "?uploadId=owned", 200, true}, {"object payload", http.MethodGet, "", 200, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := strings.Repeat("x", 2<<20)
			transport := objectMetadataTransport{base: objectSessionHTTP(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
			})}
			request, err := http.NewRequestWithContext(t.Context(), test.method, "https://synthetic.test/blob"+test.query, nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := transport.RoundTrip(request)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(response.Body)
			if test.bounded {
				if err == nil || len(raw) > 1<<20 {
					t.Fatal("provider document was not bounded")
				}
			} else if err != nil || string(raw) != body {
				t.Fatal("payload was subjected to metadata bound")
			}
			if err := response.Body.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestObjectStoreRequiresCallerContext(t *testing.T) {
	store, _ := newObjectFixture(t, "")
	object := objectDescription([]byte("payload"))
	var absent context.Context
	for _, test := range []struct {
		name string
		call func() error
	}{
		{"put", func() error { _, err := store.PutIfAbsent(absent, object, strings.NewReader("payload")); return err }},
		{"open", func() error {
			body, err := store.OpenPinned(absent, StoredObject{Object: object, VersionID: "v1"})
			if body != nil {
				_ = body.Close()
			}
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) { requireObjectCode(t, test.call(), e.CodeUsage) })
	}
}

func TestObjectStoreMultipartConditionalReuseAndConflict(t *testing.T) {
	for _, test := range []struct {
		name, mode            string
		preexisting, conflict bool
		completions, lists    int
	}{
		{name: "equal existing bytes", preexisting: true, completions: 1, lists: 1},
		{name: "different existing bytes", preexisting: true, conflict: true, completions: 1, lists: 1},
		{name: "lost completion acknowledgement retries conditional key", mode: "complete-lost-retry", completions: 2},
		{name: "lost completion rejects conflicting replacement", mode: "complete-lost-conflict", conflict: true, completions: 2},
		{name: "embedded completion error races conflicting writer", mode: "embedded-conflict", conflict: true, completions: 2, lists: 1},
		{name: "409 must not restart over conflicting bytes", mode: "complete-409-conflict", conflict: true, completions: 1, lists: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, fixture := newObjectFixture(t, test.mode)
			raw := bytes.Repeat([]byte{'n'}, 5<<20+731)
			different := bytes.Repeat([]byte{'o'}, len(raw)) // Equal length forces a complete hash check.
			object := objectDescription(raw)
			fixture.mu.Lock()
			fixture.completeConflict = different
			if test.preexisting {
				fixture.data[object.Key] = bytes.Clone(raw)
				if test.conflict {
					fixture.data[object.Key] = bytes.Clone(different)
				}
			}
			fixture.mu.Unlock()
			stored, err := store.PutIfAbsent(t.Context(), object, bytes.NewReader(raw))
			if test.conflict {
				requireObjectCode(t, err, e.CodeImmutableViolation)
				if stored.Key != "" {
					t.Fatal("conflicting multipart completion issued a successful observation")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if stored.Object != object {
					t.Fatal("equal-byte reuse lost the original object binding")
				}
			}
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			expected := raw
			if test.conflict {
				expected = different
			}
			if !bytes.Equal(fixture.data[object.Key], expected) {
				t.Fatal("conditional completion or cleanup overwrote existing bytes")
			}
			if test.preexisting {
				if fixture.calls["part"] != 0 || fixture.calls["initiate"] != 0 || fixture.calls["complete"] != 0 || fixture.calls["abort"] != 0 || fixture.calls["GET"] != 1 {
					t.Fatalf("existing object uploaded multipart bytes or repeated reads: %v", fixture.calls)
				}
			} else if fixture.calls["initiate"] != 1 || fixture.calls["complete"] != test.completions || fixture.calls["abort"] != 1 || fixture.calls["list"] != test.lists || !fixture.cleanupLive {
				t.Fatalf("unexpected conditional completion/owned cleanup: %v", fixture.calls)
			}
			if len(fixture.uploads) != 0 || fixture.calls["PUT"] != 0 {
				t.Fatal("multipart reuse/conflict left an upload or overwrote via PUT")
			}

		})
	}
}

func TestObjectStoreSinglePutRetryStopsOnConflictingReadback(t *testing.T) {
	store, fixture := newObjectFixture(t, "put-retry-conflict")
	raw := []byte("new bytes")
	different := []byte("old bytes")
	fixture.mu.Lock()
	fixture.completeConflict = different
	fixture.mu.Unlock()
	stored, err := store.PutIfAbsent(t.Context(), objectDescription(raw), bytes.NewReader(raw))
	requireObjectCode(t, err, e.CodeImmutableViolation)
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if stored.Key != "" || fixture.calls["PUT"] != 2 || fixture.calls["HEAD"] != 1 || fixture.calls["GET"] != 1 || !bytes.Equal(fixture.data["blob"], different) {
		t.Fatalf("conflicting retry continued or issued an observation: %v", fixture.calls)
	}
}

func TestObjectStoreRequiresExactGETPinEcho(t *testing.T) {
	for _, test := range []struct{ name, version, etag, echo string }{
		{"different version", "v1", "", "wrong-version"}, {"missing version", "v1", "", "missing-version"}, {"different ETag", "", `"stored"`, "wrong-etag"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, fixture := newObjectFixture(t, "unversioned")
			raw := []byte("payload")
			object := objectDescription(raw)
			fixture.mu.Lock()
			fixture.data[object.Key] = raw
			fixture.getEcho = test.echo
			fixture.mu.Unlock()
			body, err := store.OpenPinned(t.Context(), StoredObject{Object: object, VersionID: test.version, ETag: test.etag})
			requireObjectCode(t, err, e.CodeVerifyFailed)
			if body != nil {
				_ = body.Close()
				t.Fatal("different pin issued body")
			}
		})
	}
}

func TestObjectStoreLocalFailureDoesNotInventS3Status(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			want := e.CodeUnavailable
			if canceled {
				cancel()
				want = e.CodeTimeout
			}
			err := objectFailure(ctx, errors.New("synthetic-secret-local-detail"), "local operation could not finish")
			requireObjectCode(t, err, want)
			if strings.Contains(err.Error(), "S3 status 0") || strings.Contains(err.Error(), "synthetic-secret") || (!canceled && !strings.Contains(err.Error(), "no S3 response status available")) {
				t.Fatal("local error invented a service response or leaked detail")
			}
		})
	}
}
