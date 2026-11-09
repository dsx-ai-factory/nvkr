// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/resultspublish"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/resultspublish/storetest"
)

const (
	objectName    = "groups/nvkr/nvke/nvke-m2/0000000000000000001-101-1-202-1/started.json"
	sentinelToken = "sentinel-access-token"
)

// apiError is a GCS error whose text names the bucket, as the SDK's can.
func apiError(code int) error {
	return &googleapi.Error{Code: code, Message: "gs://" + sentinelBucket + "/" + objectName + " refused"}
}

// scripted fails each attempt with the next error, then succeeds.
func scripted(errs ...error) (func(context.Context, string, []byte) error, *int) {
	calls := 0
	return func(context.Context, string, []byte) error {
		calls++
		if calls <= len(errs) {
			return errs[calls-1]
		}
		return nil
	}, &calls
}

func TestResultsPublishGCS(t *testing.T) {
	retried := []time.Duration{2 * time.Second}
	for _, tc := range []struct {
		name     string
		errs     []error
		code     e.Code
		exists   bool
		attempts int
		waits    []time.Duration
	}{
		{name: "503 is retried", errs: []error{apiError(http.StatusServiceUnavailable)}, attempts: 2, waits: retried},
		{name: "429 is retried", errs: []error{apiError(http.StatusTooManyRequests)}, attempts: 2, waits: retried},
		{name: "a wrapped 503 is retried", errs: []error{fmt.Errorf("uploading: %w", apiError(http.StatusServiceUnavailable))}, attempts: 2, waits: retried},
		{name: "a joined 503 is retried", errs: []error{errors.Join(apiError(http.StatusServiceUnavailable), errors.New("closing"))}, attempts: 2, waits: retried},
		{name: "an attempt's deadline is retried", errs: []error{context.DeadlineExceeded}, attempts: 2, waits: retried},
		{name: "an unavailable token endpoint is retried", errs: []error{tokenError{status: http.StatusServiceUnavailable}}, attempts: 2, waits: retried},
		{name: "a throttled token endpoint is retried", errs: []error{tokenError{status: http.StatusTooManyRequests}}, attempts: 2, waits: retried},
		{name: "a timed-out token request is retried", errs: []error{tokenError{status: http.StatusRequestTimeout}}, attempts: 2, waits: retried},
		{name: "a token request with no response is retried", errs: []error{tokenError{status: noTokenResponse}}, attempts: 2, waits: retried},
		{name: "412 means the object exists", errs: []error{apiError(http.StatusPreconditionFailed)}, exists: true, attempts: 1},
		{name: "403 is not retried", errs: []error{apiError(http.StatusForbidden)}, code: e.CodeTrustPolicyMissing, attempts: 1},
		{name: "401 is not retried", errs: []error{apiError(http.StatusUnauthorized)}, code: e.CodeTrustPolicyMissing, attempts: 1},
		{name: "a refused token is not retried", errs: []error{tokenError{status: http.StatusForbidden}}, code: e.CodeTrustPolicyMissing, attempts: 1},
		{name: "a rejected token exchange is not retried", errs: []error{tokenError{status: http.StatusBadRequest}}, code: e.CodeTrustPolicyMissing, attempts: 1},
		{name: "a token fetch that made no request is not retried", errs: []error{tokenError{status: noTokenRequest}}, code: e.CodeTrustPolicyMissing, attempts: 1},
		{name: "a token response that could not be read is not retried", errs: []error{tokenError{status: http.StatusOK}}, code: e.CodeUnavailable, attempts: 1},
		{name: "a missing bucket", errs: []error{storage.ErrBucketNotExist}, code: e.CodeTrustPolicyMissing, attempts: 1},
		{name: "404 on a write is a missing bucket", errs: []error{apiError(http.StatusNotFound)}, code: e.CodeTrustPolicyMissing, attempts: 1},
		{name: "an unknown error is not retried", errs: []error{errors.New(sentinelBucket)}, code: e.CodeUnavailable, attempts: 1},
		{name: "retries run out", errs: slices.Repeat([]error{apiError(http.StatusBadGateway)}, gcsAttempts), code: e.CodeUnavailable,
			attempts: gcsAttempts, waits: []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			create, calls := scripted(tc.errs...)
			var waits []time.Duration
			store := &gcsStore{create: create, wait: func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }}
			err := store.Create(t.Context(), objectName, []byte("{}"))
			switch {
			case tc.exists:
				if !errors.Is(err, resultspublish.ErrExists) {
					t.Fatalf("got %v, want ErrExists", err)
				}
			case tc.code != "":
				requireStoreCode(t, err, tc.code)
			case err != nil:
				t.Fatal(err)
			}
			if *calls != tc.attempts || !slices.Equal(waits, tc.waits) {
				t.Fatalf("attempts %d, waits %v; want %d, %v", *calls, waits, tc.attempts, tc.waits)
			}
		})
	}
	t.Run("an interrupted wait stops the retries", func(t *testing.T) {
		create, _ := scripted(apiError(http.StatusServiceUnavailable))
		store := &gcsStore{create: create, wait: func(context.Context, time.Duration) error { return context.Canceled }}
		requireStoreCode(t, store.Create(t.Context(), objectName, nil), e.CodeTimeout)
	})
	t.Run("a canceled run is not retried", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		create, calls := scripted(context.Canceled)
		store := &gcsStore{create: create, wait: func(context.Context, time.Duration) error { t.Fatal("waited"); return nil }}
		requireStoreCode(t, store.Create(ctx, objectName, nil), e.CodeTimeout)
		if *calls != 1 {
			t.Fatalf("attempts %d", *calls)
		}
	})
}

// fetchingProvider makes one token request through client, as the auth library
// does, and fails with the library's kind of text for any status but 200. With no
// url it fails without a request. A background url is requested afterwards outside
// the fetch's context, as a lookup the library starts itself would be.
type fetchingProvider struct {
	client     *http.Client
	url        string
	background string
	outside    context.Context
}

func (p fetchingProvider) Token(ctx context.Context) (*auth.Token, error) {
	if p.url == "" {
		return nil, errors.New("credentials: no credential source")
	}
	status, err := p.get(ctx, p.url)
	if err != nil {
		return nil, fmt.Errorf("credentials: %w", err)
	}
	if p.background != "" {
		if _, err := p.get(p.outside, p.background); err != nil {
			return nil, err
		}
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("credentials: status code %d: %s", status, sentinelBucket)
	}
	return &auth.Token{Value: "token"}, nil
}

func (p fetchingProvider) get(ctx context.Context, url string) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return 0, err
	}
	response, err := p.client.Do(request)
	if err != nil {
		return 0, err
	}
	_ = response.Body.Close()
	return response.StatusCode, nil
}

func TestResultsPublishGCSTokenStatus(t *testing.T) {
	// The server answers each request with the status its path names.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		if err != nil {
			status = http.StatusTeapot
		}
		w.WriteHeader(status)
	}))
	defer server.Close()
	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()
	client := &http.Client{Transport: statusTransport{base: http.DefaultTransport}}
	fetch := func(provider fetchingProvider) error {
		provider.client, provider.outside = client, t.Context()
		_, err := statusTokens{tokens: provider}.Token(t.Context())
		return err
	}
	requireTokenStatus(t, fetch(fetchingProvider{url: server.URL + "/403"}), http.StatusForbidden)
	requireTokenStatus(t, fetch(fetchingProvider{url: server.URL + "/503"}), http.StatusServiceUnavailable)
	requireTokenStatus(t, fetch(fetchingProvider{url: closed.URL}), noTokenResponse)
	requireTokenStatus(t, fetch(fetchingProvider{}), noTokenRequest)
	// A request outside the fetch, on the same client, never becomes its status.
	requireTokenStatus(t, fetch(fetchingProvider{url: server.URL + "/503", background: server.URL + "/403"}), http.StatusServiceUnavailable)
	token, err := statusTokens{tokens: fetchingProvider{client: client, url: server.URL + "/200"}}.Token(t.Context())
	if err != nil || token.Value != "token" {
		t.Fatalf("got %v, %v", token, err)
	}
}

func requireTokenStatus(t *testing.T, err error, want int) {
	t.Helper()
	var token tokenError
	if !errors.As(err, &token) || token.status != want || strings.Contains(err.Error(), sentinelBucket) {
		t.Fatalf("got %v, want a token error with status %d", err, want)
	}
}

// fakeAPI is a token-exchange service and a Cloud Storage endpoint that the SDK
// itself talks to. The exchange answers tokenStatus, or issues a token when that is
// zero. Uploads are create-only and reads return what was stored; while statuses
// remain, they answer storage requests instead, in order. Every refusal names the
// bucket, as a real one can.
type fakeAPI struct {
	tokenStatus int
	mu          sync.Mutex
	statuses    []int
	objects     map[string][]byte
	exchanges   int
	requests    int
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	refuse := func(status int) {
		http.Error(w, fmt.Sprintf(`{"error":{"code":%d,"message":"gs://%s refused"}}`, status, sentinelBucket), status)
	}
	if r.URL.Path == "/v1/token" {
		f.exchanges++
		if f.tokenStatus != 0 {
			refuse(f.tokenStatus)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"`+sentinelToken+`","issued_token_type":"urn:ietf:params:oauth:token-type:access_token","token_type":"Bearer","expires_in":3600}`)
		return
	}
	f.requests++
	if r.Header.Get("Authorization") != "Bearer "+sentinelToken {
		refuse(http.StatusUnauthorized)
		return
	}
	if len(f.statuses) > 0 {
		status := f.statuses[0]
		f.statuses = f.statuses[1:]
		refuse(status)
		return
	}
	objects := "/" + sentinelBucket + "/"
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/upload/storage/v1/b/"+sentinelBucket+"/o":
		name, data, err := upload(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, ok := f.objects[name]; ok && r.URL.Query().Get("ifGenerationMatch") == "0" {
			refuse(http.StatusPreconditionFailed)
			return
		}
		f.objects[name] = data
		// The SDK checks the stored object's CRC32C against the bytes it sent.
		sum := binary.BigEndian.AppendUint32(nil, crc32.Checksum(data, crc32.MakeTable(crc32.Castagnoli)))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"bucket":%q,"name":%q,"generation":"1","size":"%d","crc32c":%q}`, sentinelBucket, name, len(data),
			base64.StdEncoding.EncodeToString(sum))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, objects):
		data, ok := f.objects[strings.TrimPrefix(r.URL.Path, objects)]
		if !ok {
			refuse(http.StatusNotFound)
			return
		}
		_, _ = w.Write(data)
	default:
		http.Error(w, "unexpected request", http.StatusTeapot)
	}
}

func (f *fakeAPI) counts() (exchanges, requests int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.exchanges, f.requests
}

// upload is the name and bytes of a one-request upload: the media is the second
// part of its multipart body.
func upload(r *http.Request) (string, []byte, error) {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.HasPrefix(mediaType, "multipart/") || r.URL.Query().Get("name") == "" {
		return "", nil, errors.New("not a named multipart upload")
	}
	parts := multipart.NewReader(r.Body, params["boundary"])
	if _, err := parts.NextPart(); err != nil {
		return "", nil, err
	}
	media, err := parts.NextPart()
	if err != nil {
		return "", nil, err
	}
	data, err := io.ReadAll(media)
	return r.URL.Query().Get("name"), data, err
}

// federated writes an external-account credential like the one the auth step
// writes, whose token exchange goes to api, and returns its path.
func federated(t *testing.T, api string) string {
	t.Helper()
	dir := t.TempDir()
	subject := filepath.Join(dir, "subject-token")
	if err := os.WriteFile(subject, []byte("subject-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{
		"type":               "external_account",
		"audience":           "//iam.googleapis.com/projects/0/locations/global/workloadIdentityPools/example/providers/example",
		"subject_token_type": "urn:ietf:params:oauth:token-type:jwt",
		"token_url":          api + "/v1/token",
		"credential_source":  map[string]string{"file": subject},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "credential.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// sdkStore is the store the publish job opens, with the job's Application Default
// Credentials and the real SDK, against api. It never waits between attempts.
func sdkStore(t *testing.T, api *fakeAPI) *gcsStore {
	t.Helper()
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	t.Setenv("STORAGE_EMULATOR_HOST", "")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", federated(t, server.URL))
	client, err := newGCSClient(t.Context(), option.WithEndpoint(server.URL+"/storage/v1/"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	store := newGCSStore(client.Bucket(sentinelBucket))
	store.wait = func(context.Context, time.Duration) error { return nil }
	return store
}

func TestResultsPublishGCSConformance(t *testing.T) {
	storetest.Conformance(t, func(t *testing.T) resultspublish.ObjectStore {
		return sdkStore(t, &fakeAPI{objects: map[string][]byte{}})
	})
}

// TestResultsPublishGCSFederatedToken drives the auth library's own federated
// exchange, whose failures it reports only as text.
func TestResultsPublishGCSFederatedToken(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		code      e.Code
		exchanges int
	}{
		{"a refused identity is not retried", http.StatusForbidden, e.CodeTrustPolicyMissing, 1},
		{"a rejected subject token is not retried", http.StatusBadRequest, e.CodeTrustPolicyMissing, 1},
		{"an unavailable token service is retried", http.StatusServiceUnavailable, e.CodeUnavailable, gcsAttempts},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{tokenStatus: tc.status, objects: map[string][]byte{}}
			requireStoreCode(t, sdkStore(t, api).Create(t.Context(), objectName, []byte("{}")), tc.code)
			if exchanges, requests := api.counts(); exchanges != tc.exchanges || requests != 0 {
				t.Fatalf("exchanges %d, bucket requests %d; want %d, 0", exchanges, requests, tc.exchanges)
			}
		})
	}
}

// TestResultsPublishGCSResponses maps the storage responses the SDK returns, and
// checks that only the store retries.
func TestResultsPublishGCSResponses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		statuses []int
		code     e.Code
		requests int
	}{
		{"a refused identity", []int{http.StatusForbidden}, e.CodeTrustPolicyMissing, 1},
		{"a bucket that does not exist", []int{http.StatusNotFound}, e.CodeTrustPolicyMissing, 1},
		{"an unavailable store is retried", []int{http.StatusServiceUnavailable}, "", 2},
		{"retries run out", slices.Repeat([]int{http.StatusServiceUnavailable}, gcsAttempts), e.CodeUnavailable, gcsAttempts},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeAPI{statuses: slices.Clone(tc.statuses), objects: map[string][]byte{}}
			err := sdkStore(t, api).Create(t.Context(), objectName, []byte("{}"))
			if tc.code == "" && err != nil {
				t.Fatal(err)
			}
			if tc.code != "" {
				requireStoreCode(t, err, tc.code)
			}
			if _, requests := api.counts(); requests != tc.requests {
				t.Fatalf("requests %d, want %d", requests, tc.requests)
			}
		})
	}
	t.Run("a read is retried only by the store", func(t *testing.T) {
		api := &fakeAPI{objects: map[string][]byte{objectName: []byte("{}")}}
		store := sdkStore(t, api)
		api.mu.Lock()
		api.statuses = slices.Repeat([]int{http.StatusServiceUnavailable}, gcsAttempts)
		api.mu.Unlock()
		// The SDK's own retries would run until each attempt's deadline.
		ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
		defer cancel()
		_, err := store.Read(ctx, objectName, resultspublish.MaxReadBytes)
		requireStoreCode(t, err, e.CodeUnavailable)
		if _, requests := api.counts(); requests != gcsAttempts {
			t.Fatalf("requests %d, want %d", requests, gcsAttempts)
		}
	})
}

func TestResultsPublishGCSRead(t *testing.T) {
	reader := func(data []byte, errs ...error) func(context.Context, string) (io.ReadCloser, error) {
		calls := 0
		return func(context.Context, string) (io.ReadCloser, error) {
			calls++
			if calls <= len(errs) {
				return nil, errs[calls-1]
			}
			return io.NopCloser(bytes.NewReader(data)), nil
		}
	}
	noWait := func(context.Context, time.Duration) error { return nil }
	t.Run("a read within the bound", func(t *testing.T) {
		store := &gcsStore{read: reader([]byte("12345"), apiError(http.StatusServiceUnavailable)), wait: noWait}
		got, err := store.Read(t.Context(), objectName, 5)
		if err != nil || string(got) != "12345" {
			t.Fatalf("got %q, %v", got, err)
		}
	})
	t.Run("a read over the bound", func(t *testing.T) {
		store := &gcsStore{read: reader([]byte("123456")), wait: noWait}
		_, err := store.Read(t.Context(), objectName, 5)
		requireStoreCode(t, err, e.CodeImmutableViolation)
	})
	t.Run("a vanished object", func(t *testing.T) {
		store := &gcsStore{read: reader(nil, storage.ErrObjectNotExist), wait: noWait}
		_, err := store.Read(t.Context(), objectName, 5)
		requireStoreCode(t, err, e.CodeUnavailable)
	})
}

func TestResultsPublishContentType(t *testing.T) {
	for name, want := range map[string]string{"a/outcome.json": "application/json", "a/artifacts/junit.xml": "application/xml", "a/b": "application/octet-stream"} {
		if got := contentType(name); got != want {
			t.Fatalf("%s: %s, want %s", name, got, want)
		}
	}
}

// requireStoreCode also checks that no SDK text, which can name the bucket, and no
// token leaks.
func requireStoreCode(t *testing.T, err error, code e.Code) {
	t.Helper()
	var coded *e.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatalf("got %v, want %s", err, code)
	}
	if strings.Contains(err.Error(), sentinelBucket) || strings.Contains(err.Error(), sentinelToken) {
		t.Fatalf("error names the bucket or token: %v", err)
	}
}
