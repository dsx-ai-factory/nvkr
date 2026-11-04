// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/arn"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// S3ObjectStoreOptions supplies one operation's literal destination and runtime
// identity. Exactly one of Credentials or RoleARN+TokenSource is required. The
// Object.Key and StoredObject.Key are relative to Prefix, which is joined once
// only at provider dispatch. The caller's credentials must be temporary; no default chain or credential file is
// consulted. The workload source must honor ctx and join its I/O before return.
// The single session must cover OperationTimeout+CleanupTimeout; it never refreshes.
// Zero tuning fields select conservative defaults; secrets never belong in authored data.
type S3ObjectStoreOptions struct {
	Region, Endpoint, Bucket, Prefix, ExpectedBucketOwner, Addressing string
	RoleARN, SessionName                                              string
	TokenSource                                                       func(context.Context) ([]byte, error)
	Credentials                                                       *aws.Credentials
	OperationTimeout, CleanupTimeout                                  time.Duration
	PartSizeBytes                                                     int64
	MaxAttempts, MaxUploadAttempts                                    int
}

type s3ObjectAPI interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	CreateMultipartUpload(context.Context, *s3.CreateMultipartUploadInput, ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error)
	UploadPart(context.Context, *s3.UploadPartInput, ...func(*s3.Options)) (*s3.UploadPartOutput, error)
	CompleteMultipartUpload(context.Context, *s3.CompleteMultipartUploadInput, ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error)
	AbortMultipartUpload(context.Context, *s3.AbortMultipartUploadInput, ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error)
	ListParts(context.Context, *s3.ListPartsInput, ...func(*s3.Options)) (*s3.ListPartsOutput, error)
}

type s3ObjectStore struct {
	options   S3ObjectStoreOptions
	api       s3ObjectAPI
	lifetime  context.Context
	cancel    context.CancelFunc
	transport *http.Transport
	mu        sync.Mutex
	active    sync.WaitGroup
	closed    bool
	bodies    map[*objectResponseBody]struct{}
}

func newS3ObjectStore(ctx context.Context, options S3ObjectStoreOptions) (ObjectStore, error) {
	options, err := admitObjectOptions(options)
	if err != nil {
		return nil, err
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, objectCapability("S3 requires a standard owned HTTP transport")
	}
	transport := base.Clone()
	transport.ResponseHeaderTimeout = min(30*time.Second, options.OperationTimeout)
	lifetime, cancel := context.WithTimeout(ctx, options.OperationTimeout)
	client := &http.Client{Transport: objectMetadataTransport{transport}, Timeout: options.OperationTimeout + options.CleanupTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	session, err := objectSession(lifetime, options, client)
	if err != nil {
		cancel()
		transport.CloseIdleConnections()
		return nil, err
	}
	store := &s3ObjectStore{options: options, lifetime: lifetime, cancel: cancel, transport: transport, bodies: make(map[*objectResponseBody]struct{})}
	store.api = s3.New(s3.Options{Region: options.Region, BaseEndpoint: optionalObjectString(options.Endpoint), UsePathStyle: options.Addressing == "path",
		Credentials: session, HTTPClient: client, Retryer: retry.NewStandard(func(o *retry.StandardOptions) { o.MaxAttempts = options.MaxAttempts }),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenSupported, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenSupported})
	return store, nil
}

func admitObjectOptions(o S3ObjectStoreOptions) (S3ObjectStoreOptions, error) {
	if o.OperationTimeout == 0 {
		o.OperationTimeout = time.Hour - time.Minute - objectSessionAllowance
	}
	if o.CleanupTimeout == 0 {
		o.CleanupTimeout = time.Minute
	}
	if o.PartSizeBytes == 0 {
		o.PartSizeBytes = 8 << 20
	}
	if o.MaxAttempts == 0 {
		o.MaxAttempts = 3
	}
	if o.MaxUploadAttempts == 0 {
		o.MaxUploadAttempts = 2
	}
	if o.Addressing == "" {
		o.Addressing = "virtual"
	}
	if o.Region == "" || o.Bucket == "" || !objectText(o.Region) || !objectText(o.Bucket) || strings.ContainsAny(o.Bucket, "/\\?# \t") || !validObjectKey(o.Prefix) ||
		(o.Addressing != "path" && o.Addressing != "virtual") || o.OperationTimeout <= 0 || o.OperationTimeout > 11*time.Hour || o.CleanupTimeout <= 0 || o.CleanupTimeout > time.Hour ||
		o.PartSizeBytes < 5<<20 || o.PartSizeBytes > 5<<30 || o.MaxAttempts < 1 || o.MaxAttempts > 10 || o.MaxUploadAttempts < 1 || o.MaxUploadAttempts > 10 {
		return o, e.New(e.CodeUsage, "invalid bounded S3 destination or transfer options")
	}
	if o.Endpoint != "" {
		u, err := url.Parse(o.Endpoint)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Path != "" || strings.ContainsAny(o.Endpoint, "?#") || !objectText(o.Endpoint) {
			return o, e.New(e.CodeUsage, "S3 endpoint must be an HTTPS origin")
		}
	}
	if len(o.ExpectedBucketOwner) != 12 || strings.Trim(o.ExpectedBucketOwner, "0123456789") != "" {
		return o, e.New(e.CodeUsage, "S3 requires a twelve-digit expected bucket owner")
	}
	explicit := o.Credentials != nil
	workload := o.RoleARN != "" && o.TokenSource != nil
	if explicit == workload || (explicit && (o.RoleARN != "" || o.TokenSource != nil)) || (!explicit && !workload) || !objectText(o.RoleARN) || !objectText(o.SessionName) {
		return o, e.New(e.CodeTrustPolicyMissing, "select exactly one temporary S3 identity")
	}
	if workload {
		parsed, err := arn.Parse(o.RoleARN)
		if err != nil || parsed.Service != "iam" || !strings.HasPrefix(parsed.Resource, "role/") || parsed.AccountID == "" {
			return o, e.New(e.CodeTrustPolicyMissing, "workload identity requires a valid IAM role ARN")
		}
	}
	return o, nil
}

func optionalObjectString(value string) *string {
	if value == "" {
		return nil
	}
	return aws.String(value)
}
func objectText(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}
func validObjectKey(key string) bool {
	return key != "." && len(key) <= 1024 && fs.ValidPath(key) && !strings.Contains(key, "\\") && objectText(key)
}
func (store *s3ObjectStore) physicalKey(key string) string { return store.options.Prefix + "/" + key }

func (store *s3ObjectStore) validate(object Object) error {
	if !validObjectKey(object.Key) || len(store.physicalKey(object.Key)) > 1024 || !canonical.IsDigest(object.SHA256) || object.Size < 0 ||
		(object.Size > 0 && (object.Size-1)/(5<<30) >= 10000) {
		return e.New(e.CodeUsage, "object requires an in-prefix key, exact SHA256 and bounded multipart size")
	}
	if _, _, err := mime.ParseMediaType(object.MediaType); err != nil {
		return e.New(e.CodeUsage, "object requires a valid media type")
	}
	return nil
}

func (store *s3ObjectStore) partSize(size int64) int64 {
	return max(store.options.PartSizeBytes, (size-1)/10000+1)
}

func (store *s3ObjectStore) begin(ctx context.Context) (context.Context, func(), error) {
	if err := deliveryContext(ctx); err != nil {
		return nil, nil, err
	}
	store.mu.Lock()
	if store.closed || store.lifetime.Err() != nil {
		store.mu.Unlock()
		return nil, nil, e.New(e.CodeTimeout, "object-store lifetime ended")
	}
	store.active.Add(1)
	store.mu.Unlock()
	deadline, _ := store.lifetime.Deadline()
	owned, cancel := context.WithDeadline(ctx, deadline)
	joined := make(chan struct{})
	stop := context.AfterFunc(store.lifetime, func() { cancel(); close(joined) })
	finish := func() {
		if !stop() {
			<-joined
		}
		cancel()
		store.active.Done()
	}
	if err := deliveryContext(owned); err != nil {
		finish()
		return nil, nil, err
	}
	return owned, finish, nil
}

// Close revokes work, closes borrowed HTTP responses, joins calls and releases
// only this store's transport pool. All SDK calls and cleanup are synchronous.
func (store *s3ObjectStore) Close() error {
	store.mu.Lock()
	store.closed = true
	store.cancel()
	bodies := make([]*objectResponseBody, 0, len(store.bodies))
	for body := range store.bodies {
		bodies = append(bodies, body)
	}
	store.mu.Unlock()
	var err error
	for _, body := range bodies {
		err = errors.Join(err, body.Close())
	}
	store.active.Wait()
	if store.transport != nil {
		store.transport.CloseIdleConnections()
	}
	return err
}

func (store *s3ObjectStore) PutIfAbsent(ctx context.Context, object Object, body io.ReadSeeker) (StoredObject, error) {
	stored, _, err := store.putIfAbsentWithReadback(ctx, object, body)
	return stored, err
}

func (store *s3ObjectStore) putIfAbsentWithReadback(ctx context.Context, object Object, body io.ReadSeeker) (StoredObject, *objectReadback, error) {
	if err := store.validate(object); err != nil {
		return StoredObject{}, nil, err
	}
	if body == nil {
		return StoredObject{}, nil, e.New(e.CodeUsage, "object upload requires an immutable replayable source")
	}
	ctx, finish, err := store.begin(ctx)
	if err != nil {
		return StoredObject{}, nil, err
	}
	defer finish()
	if object.Size > store.options.PartSizeBytes {
		// Successful HEAD can avoid sending every part on a rerun. Missing-key
		// 403/404 is only permission to try conditional creation, never absence proof.
		head, headErr := store.api.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(store.options.Bucket), ExpectedBucketOwner: aws.String(store.options.ExpectedBucketOwner), Key: aws.String(store.physicalKey(object.Key))})
		if headErr == nil {
			expected, err := store.headObservation(StoredObject{Object: object}, head)
			if err != nil {
				return StoredObject{}, nil, err
			}
			return store.reconcilePinned(ctx, expected)
		}
		if status := objectStatus(headErr); status != 403 && status != 404 {
			return StoredObject{}, nil, objectFailure(ctx, headErr, "S3 existing-object probe failed")
		}
		return store.putMultipart(ctx, object, body)
	}
	for attempt := 0; attempt < store.options.MaxUploadAttempts; attempt++ {
		if _, err := body.Seek(0, io.SeekStart); err != nil {
			return StoredObject{}, nil, e.New(e.CodeVerifyFailed, "cannot rewind object source")
		}
		out, putErr := store.api.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(store.options.Bucket), ExpectedBucketOwner: aws.String(store.options.ExpectedBucketOwner), Key: aws.String(store.physicalKey(object.Key)),
			Body: body, ContentLength: aws.Int64(object.Size), ContentType: aws.String(object.MediaType), IfNoneMatch: aws.String("*")})
		if putErr == nil {
			if out == nil {
				return StoredObject{}, nil, objectFailure(ctx, nil, "empty S3 PUT response")
			}
			return StoredObject{Object: object, VersionID: aws.ToString(out.VersionId), ETag: aws.ToString(out.ETag)}, nil, nil
		}
		if !objectMayHaveCommitted(putErr) {
			return StoredObject{}, nil, objectFailure(ctx, putErr, "S3 conditional PUT failed")
		}
		observed, readback, reconcileErr := store.reconcile(ctx, object)
		if reconcileErr == nil {
			return observed, readback, nil
		}
		if objectConflict(reconcileErr) || objectStatus(putErr) == 412 {
			return StoredObject{}, nil, reconcileErr
		}
		if ctx.Err() != nil {
			return StoredObject{}, nil, objectFailure(ctx, ctx.Err(), "S3 PUT outcome remains unknown")
		}
	}
	return StoredObject{}, nil, objectFailure(ctx, nil, "S3 PUT budget exhausted; remote outcome remains unknown")
}

func (store *s3ObjectStore) observe(ctx context.Context, expected StoredObject) (StoredObject, error) {
	if expected.VersionID != "" {
		return expected, nil
	}
	if expected.ETag != "" {
		if !validObjectETag(expected.ETag) {
			return StoredObject{}, e.New(e.CodeVerifyFailed, "object requires one strong ETag")
		}
		return expected, nil
	}
	out, err := store.api.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(store.options.Bucket), ExpectedBucketOwner: aws.String(store.options.ExpectedBucketOwner), Key: aws.String(store.physicalKey(expected.Key))})
	if err != nil {
		return StoredObject{}, objectFailure(ctx, err, "S3 HEAD failed; absence is unknown")
	}
	return store.headObservation(expected, out)
}
func (store *s3ObjectStore) headObservation(expected StoredObject, out *s3.HeadObjectOutput) (StoredObject, error) {
	if out == nil || out.ContentLength == nil || *out.ContentLength != expected.Size {
		return StoredObject{}, e.New(e.CodeImmutableViolation, "existing object size differs")
	}
	expected.VersionID = aws.ToString(out.VersionId)
	expected.ETag = aws.ToString(out.ETag)
	if expected.VersionID == "" && !validObjectETag(expected.ETag) {
		return StoredObject{}, e.New(e.CodeVerifyFailed, "S3 object has no exact version or strong ETag")
	}
	return expected, nil
}

// OpenPinned returns a bounded provider stream. SHA256 verification remains with
// the owning publication; an ETag is an opaque precondition, never a blob digest.
func (store *s3ObjectStore) OpenPinned(ctx context.Context, expected StoredObject) (io.ReadCloser, error) {
	if err := store.validate(expected.Object); err != nil {
		return nil, err
	}
	ctx, finish, err := store.begin(ctx)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			finish()
		}
	}()
	expected, err = store.observe(ctx, expected)
	if err != nil {
		return nil, err
	}
	in := &s3.GetObjectInput{Bucket: aws.String(store.options.Bucket), ExpectedBucketOwner: aws.String(store.options.ExpectedBucketOwner), Key: aws.String(store.physicalKey(expected.Key))}
	if expected.VersionID != "" {
		in.VersionId = aws.String(expected.VersionID)
	} else {
		in.IfMatch = aws.String(expected.ETag)
	}
	out, err := store.api.GetObject(ctx, in)
	if err != nil {
		return nil, objectFailure(ctx, err, "S3 exact read failed without fallback")
	}
	if out == nil || out.Body == nil {
		return nil, e.New(e.CodeVerifyFailed, "S3 GET returned no body")
	}
	if out.ContentLength == nil || *out.ContentLength != expected.Size || (in.VersionId != nil && aws.ToString(out.VersionId) != expected.VersionID) || (in.IfMatch != nil && aws.ToString(out.ETag) != expected.ETag) {
		_ = out.Body.Close()
		return nil, e.New(e.CodeVerifyFailed, "S3 GET differs from requested size or pin")
	}
	body := &objectResponseBody{body: out.Body, ctx: ctx, store: store, finish: finish}
	store.mu.Lock()
	if store.closed {
		store.mu.Unlock()
		_ = out.Body.Close()
		return nil, e.New(e.CodeTimeout, "object store closed during GET")
	}
	store.bodies[body] = struct{}{}
	store.mu.Unlock()
	success = true
	return body, nil
}

func (store *s3ObjectStore) reconcile(ctx context.Context, object Object) (StoredObject, *objectReadback, error) {
	expected, err := store.observe(ctx, StoredObject{Object: object})
	if err != nil {
		return StoredObject{}, nil, err
	}
	return store.reconcilePinned(ctx, expected)
}
func (store *s3ObjectStore) reconcilePinned(ctx context.Context, expected StoredObject) (StoredObject, *objectReadback, error) {
	object := expected.Object
	body, err := store.OpenPinned(ctx, expected)
	if err != nil {
		return StoredObject{}, nil, err
	}
	hash := sha256.New()
	size, readErr := io.Copy(hash, io.LimitReader(body, object.Size+1))
	closeErr := body.Close()
	if readErr != nil || closeErr != nil {
		return StoredObject{}, nil, objectFailure(ctx, errors.Join(readErr, closeErr), "S3 reconciliation read failed")
	}
	if size != object.Size || "sha256:"+hex.EncodeToString(hash.Sum(nil)) != object.SHA256 {
		return StoredObject{}, nil, e.New(e.CodeImmutableViolation, "existing object bytes differ from requested SHA256 or size")
	}
	if err := deliveryContext(ctx); err != nil {
		return StoredObject{}, nil, err
	}
	return expected, &objectReadback{store: store, stored: expected}, nil
}

type objectResponseBody struct {
	body   io.ReadCloser
	ctx    context.Context
	store  *s3ObjectStore
	finish func()
	once   sync.Once
	err    error
}

func (body *objectResponseBody) Read(p []byte) (int, error) {
	if err := deliveryContext(body.ctx); err != nil {
		return 0, err
	}
	n, err := body.body.Read(p)
	if contextErr := deliveryContext(body.ctx); contextErr != nil {
		return n, contextErr
	}
	if err != nil && err != io.EOF {
		return n, objectFailure(body.ctx, err, "S3 body read failed")
	}
	return n, err
}
func (body *objectResponseBody) Close() error {
	body.once.Do(func() {
		if err := body.body.Close(); err != nil {
			body.err = e.New(e.CodeUnavailable, "closing S3 response failed")
		}
		body.store.mu.Lock()
		delete(body.store.bodies, body)
		body.store.mu.Unlock()
		body.finish()
	})
	return body.err
}

// This is the existing cistore single quoted ETag grammar, with weak validators
// forbidden for If-Match (RFC 9110 section 13.1.1). Wildcards and lists are refused.
func validObjectETag(value string) bool {
	if len(value) < 2 || len(value) > 1024 || value[0] != '"' || value[len(value)-1] != '"' {
		return false
	}
	for i := 1; i < len(value)-1; i++ {
		if value[i] < 0x21 || value[i] == '"' || value[i] == 0x7f {
			return false
		}
	}
	return true
}
func objectStatus(err error) int {
	var response *smithyhttp.ResponseError
	if errors.As(err, &response) {
		return response.HTTPStatusCode()
	}
	return 0
}
func objectCode(err error) string {
	var api smithy.APIError
	if errors.As(err, &api) {
		return api.ErrorCode()
	}
	return ""
}
func objectMayHaveCommitted(err error) bool {
	return objectStatus(err) == 409 || objectStatus(err) == 412 || objectCode(err) == "NoSuchUpload" || retry.NewStandard().IsErrorRetryable(err)
}
func objectConflict(err error) bool {
	var coded *e.Error
	return errors.As(err, &coded) && coded.Code == e.CodeImmutableViolation
}
func objectFailure(ctx context.Context, err error, message string) error {
	if status := objectStatus(err); status != 0 {
		return classifyContext(ctx, e.New(e.CodeUnavailable, "%s (S3 status %d)", message, status))
	}
	return classifyContext(ctx, e.New(e.CodeUnavailable, "%s (no S3 response status available)", message))
}
func objectCapability(message string) error { return e.New(e.CodeEngineCapability, "%s", message) }

// SDK error/control documents are memory-decoded; successful payloads stream.
// Standard MaxBytesReader bounds those documents without a custom HTTP codec.
type objectMetadataTransport struct{ base http.RoundTripper }

func (transport objectMetadataTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, err
	}
	if response != nil && response.Body != nil && !(request.Method == http.MethodGet && response.StatusCode == http.StatusOK && request.URL.Query().Get("uploadId") == "") {
		response.Body = http.MaxBytesReader(nil, response.Body, 1<<20)
	}
	return response, nil
}
