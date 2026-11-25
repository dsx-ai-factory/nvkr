// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

// check-node-image: check that a node image URL serves the bytes the hardware E2E workflow names, as the hardware
// site will fetch them.
//
// The workflow hands the hardware site's imager a plain HTTPS URL and a digest recorded elsewhere. This fetches
// that exact URL the way the imager will: a GET with no credentials, no redirects followed and no proxy,
// streaming the whole body. It requires the digest and the declared length to match and the first bytes to be a
// plain qcow2. It runs before any machine is taken, so a URL that answers HEAD but refuses GET (GitLab artifact
// downloads do exactly that), a moved object or an expired artifact fails here instead of forty minutes into
// provisioning.
//
// What this establishes is narrow. It proves the URL served those bytes to the runner that asked, at that moment.
// It does not prove the hardware site's network can reach the host, that the imager trusts its certificate, that
// the image boots, or that the object will still be there when the run starts. Whether the server honors Range
// is recorded, not required: it is a fact about the server, and what the imager needs is a question for the
// imager. A server that refuses the Range probe outright is recorded as such, and the plain GET still decides
// whether the URL serves the named bytes.
//
// With --substitutions the URL, the digest and the method come from the substituted plan's record, where the
// outputs manifest named how each image was checked. http-get is the fetch above. s3-readback fetches nothing:
// the publication workflow already read the object back from the object store with credentials, because the
// HTTPS front end can sit behind an address allowlist that a hosted runner is not on. The record then says what
// that run reported, which run it was and that this run did not observe it. Nothing here authenticates the cited
// run; it is a description until a machine boots the image.
//
// Exit codes: 0 ok, 2 usage, 13 unsupported URL, image, record or verification, 22 the URL did not serve the
// named bytes, 31 the run was interrupted, 47 the output already exists.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	nerrors "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const (
	nodeImageSchema      = "nvkr.dev/hardware-e2e-node-image-check/v1"
	substitutionsSchema  = "nvkr.dev/hardware-e2e-substitutions/v1"
	methodGet            = "http-get"
	methodReadback       = "s3-readback"
	chunkBytes           = 1 << 20
	defaultMaxBytes      = int64(64) << 30
	defaultTimeoutSecs   = 60
	defaultMaxSecs       = 1800
	nodeImageUserAgent   = "nvkr-hardware-e2e-check-node-image"
	imageURLMessage      = "the node image must be a plain https URL to a .qcow2, with no credentials, query or fragment"
	checkNodeImageUsage  = "--slot SLOT (--substitutions FILE | --url URL --sha256 DIGEST) --out FILE [--timeout SECONDS] [--max-seconds SECONDS] [--max-bytes BYTES]"
	maxVerificationBytes = 16 << 20
)

var (
	nodeImageSlots = []string{"os-image-amd64", "os-image-arm64"}
	// Plain HTTPS to a .qcow2: no credentials, query or fragment. The substitution script admits the same shape.
	imageURLPattern = regexp.MustCompile(`^https://[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]+)?/[A-Za-z0-9._~%+=/-]+\.qcow2$`)
	sha256Pattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	// The publication workflow's run, with or without its attempt.
	runURLPattern  = regexp.MustCompile(`^https://github\.com/[A-Za-z0-9._-]+/[A-Za-z0-9._-]+/actions/runs/[1-9][0-9]{0,19}(/attempts/[1-9][0-9]{0,5})?$`)
	integerPattern = regexp.MustCompile(`^-?[0-9]+$`)
	readbackKeys   = []string{"bytes", "method", "qcow2Version", "run", "virtualSizeBytes"}
)

// imageResponse is one answer to a fetch. A fetcher returns every HTTP answer, whatever its status; get decides
// which are failures.
type imageResponse struct {
	status int
	reason string
	header http.Header
	body   io.ReadCloser
	// limit bounds the next read of body, when the transport can. A scripted answer has nothing to bound.
	limit func(time.Duration)
}

// fetcher opens one URL with the given headers. perRead bounds the connection and each wait for the server.
type fetcher interface {
	open(ctx context.Context, rawURL string, header http.Header, perRead time.Duration) (*imageResponse, error)
}

// httpFetcher fetches the way the imager will: no proxy, no redirects, no content coding, one connection.
type httpFetcher struct{}

// deadlineConn gives every read the deadline its owner last set, so a server that trickles one byte inside the
// inactivity limit cannot hold a read open past the whole-body deadline.
type deadlineConn struct {
	net.Conn
	mu      sync.Mutex
	timeout time.Duration
}

func (c *deadlineConn) setTimeout(d time.Duration) {
	c.mu.Lock()
	c.timeout = d
	c.mu.Unlock()
}

func (c *deadlineConn) current() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.timeout
}

func (c *deadlineConn) Read(p []byte) (int, error) {
	if d := c.current(); d > 0 {
		if err := c.SetReadDeadline(time.Now().Add(d)); err != nil {
			return 0, err
		}
	}
	return c.Conn.Read(p)
}

func (c *deadlineConn) Write(p []byte) (int, error) {
	if d := c.current(); d > 0 {
		if err := c.SetWriteDeadline(time.Now().Add(d)); err != nil {
			return 0, err
		}
	}
	return c.Conn.Write(p)
}

// connHolder remembers the connection one fetch dialed.
type connHolder struct {
	mu   sync.Mutex
	conn *deadlineConn
}

func (h *connHolder) set(c *deadlineConn) {
	h.mu.Lock()
	h.conn = c
	h.mu.Unlock()
}

func (h *connHolder) limit(d time.Duration) {
	h.mu.Lock()
	conn := h.conn
	h.mu.Unlock()
	if conn != nil {
		conn.setTimeout(d)
	}
}

// newTransport builds a transport for exactly one request: an ambient proxy would change what is being checked,
// a content coding would change the bytes, and a kept-alive connection would outlive the check.
func newTransport(perRead time.Duration, holder *connHolder) *http.Transport {
	dialer := &net.Dialer{Timeout: perRead}
	return &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, address)
			if err != nil {
				return nil, err
			}
			bounded := &deadlineConn{Conn: conn, timeout: perRead}
			holder.set(bounded)
			return bounded, nil
		},
		DisableCompression:    true,
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   perRead,
		ResponseHeaderTimeout: perRead,
	}
}

// closingBody releases the single-use transport together with the body.
type closingBody struct {
	io.ReadCloser
	transport *http.Transport
}

func (b closingBody) Close() error {
	err := b.ReadCloser.Close()
	b.transport.CloseIdleConnections()
	return err
}

func (httpFetcher) open(ctx context.Context, rawURL string, header http.Header, perRead time.Duration) (*imageResponse, error) {
	holder := &connHolder{}
	transport := newTransport(perRead, holder)
	client := &http.Client{
		Transport: transport,
		// The imager is given one URL, so a redirect is a different URL than the one being checked.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, err
	}
	request.Header = header.Clone()
	response, err := client.Do(request) //nolint:bodyclose // the body is handed to the caller, which closes it
	if err != nil {
		transport.CloseIdleConnections()
		var urlError *url.Error
		if errors.As(err, &urlError) {
			err = urlError.Err
		}
		return nil, err
	}
	return &imageResponse{
		status: response.StatusCode,
		reason: http.StatusText(response.StatusCode),
		header: response.Header,
		body:   closingBody{ReadCloser: response.Body, transport: transport},
		limit:  holder.limit,
	}, nil
}

func baseHeader() http.Header {
	return http.Header{"User-Agent": {nodeImageUserAgent}, "Accept-Encoding": {"identity"}}
}

// imageChecker holds the transport and the limits one check runs under. now is the clock the whole-body deadline
// reads, so a test can move it.
type imageChecker struct {
	fetch    fetcher
	now      func() time.Time
	perRead  time.Duration
	whole    time.Duration
	maxBytes int64
}

func newImageChecker(fetch fetcher, now func() time.Time, perRead, whole time.Duration, maxBytes int64) *imageChecker {
	return &imageChecker{fetch: fetch, now: now, perRead: perRead, whole: whole, maxBytes: maxBytes}
}

// canceled maps an interrupted run to its own code instead of blaming the server.
func canceled(ctx context.Context, rawURL string) error {
	if err := ctx.Err(); err != nil {
		return nerrors.Wrap(nerrors.CodeTimeout, err, "the check of %s was interrupted", rawURL)
	}
	return nil
}

// get returns an open response for a 2xx answer and turns everything else into a coded failure.
func (c *imageChecker) get(ctx context.Context, rawURL string, header http.Header) (*imageResponse, error) {
	response, err := c.fetch.open(ctx, rawURL, header, c.perRead)
	if err != nil {
		if interrupted := canceled(ctx, rawURL); interrupted != nil {
			return nil, interrupted
		}
		return nil, verifyFailed("%s could not be fetched: %v", rawURL, err)
	}
	if response.status >= 200 && response.status < 300 {
		return response, nil
	}
	_ = response.body.Close()
	what := "an error"
	if response.status >= 300 && response.status < 400 {
		what = "a redirect, which is not followed"
	}
	return nil, verifyFailed("%s answered HTTP %d %s, %s", rawURL, response.status, response.reason, what)
}

// probeRange asks for one byte. Recorded, never required.
//
// A 206 means the server honors Range, a 200 that it ignored the header, and anything else that the probe was
// unavailable: a server may reject a Range request and still serve the whole object. The plain GET that follows
// decides whether the URL serves the named bytes.
func (c *imageChecker) probeRange(ctx context.Context, rawURL string) (probe, accept string) {
	header := baseHeader()
	header.Set("Range", "bytes=0-0")
	response, err := c.get(ctx, rawURL, header)
	if err != nil {
		return "unavailable", ""
	}
	defer func() { _ = response.body.Close() }()
	accept = response.header.Get("Accept-Ranges")
	switch response.status {
	case http.StatusPartialContent:
		return "honored", accept
	case http.StatusOK:
		return "ignored", accept
	}
	return "unavailable", accept
}

func (c *imageChecker) stillStreaming(rawURL string, count, declared int64) error {
	return verifyFailed("%s was still streaming after %gs, at %d of %d bytes", rawURL, c.whole.Seconds(), count, declared)
}

func (c *imageChecker) stream(ctx context.Context, rawURL, expectedSHA256 string) (int64, qcow2Info, error) {
	started := c.now()
	response, err := c.get(ctx, rawURL, baseHeader())
	if err != nil {
		return 0, qcow2Info{}, err
	}
	defer func() { _ = response.body.Close() }()
	if response.status != http.StatusOK {
		return 0, qcow2Info{}, verifyFailed("%s answered HTTP %d to a plain GET, not 200", rawURL, response.status)
	}
	if encoding := response.header.Get("Content-Encoding"); strings.ToLower(encoding) != "" && strings.ToLower(encoding) != "identity" {
		return 0, qcow2Info{}, verifyFailed("%s is served with Content-Encoding %s, so the bytes on the wire are not the image", rawURL, encoding)
	}
	text := response.header.Get("Content-Length")
	declared, parseErr := strconv.ParseInt(text, 10, 64)
	if errors.Is(parseErr, strconv.ErrRange) && asciiDigits(text) {
		// No 64-bit count holds it, so it is over the limit whatever the limit is.
		return 0, qcow2Info{}, verifyFailed("%s declares %s bytes, over the %d byte limit", rawURL, text, c.maxBytes)
	}
	if parseErr != nil || !asciiDigits(text) || declared == 0 {
		return 0, qcow2Info{}, verifyFailed("%s declares no usable Content-Length (%q)", rawURL, text)
	}
	if declared > c.maxBytes {
		return 0, qcow2Info{}, verifyFailed("%s declares %d bytes, over the %d byte limit", rawURL, declared, c.maxBytes)
	}

	digest := sha256.New()
	var count int64
	var head []byte
	var info *qcow2Info
	buffer := make([]byte, chunkBytes)
	for {
		remaining := c.whole - c.now().Sub(started)
		if remaining <= 0 {
			return 0, qcow2Info{}, c.stillStreaming(rawURL, count, declared)
		}
		if response.limit != nil {
			// A socket timeout restarts with every byte, so each read gets the smaller of the inactivity limit and
			// what is left of the whole-body deadline.
			response.limit(min(c.perRead, remaining))
		}
		n, readErr := response.body.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			count += int64(n)
			if count > declared {
				return 0, qcow2Info{}, verifyFailed("%s sent more than its declared %d bytes", rawURL, declared)
			}
			digest.Write(chunk)
			if info == nil {
				head = append(head, chunk...)
				if len(head) >= qcow2HeaderLength(head) {
					parsed, err := parseQCOW2Header(head)
					if err != nil {
						return 0, qcow2Info{}, err
					}
					info = &parsed
				}
			}
		}
		if readErr == nil {
			continue
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if interrupted := canceled(ctx, rawURL); interrupted != nil {
			return 0, qcow2Info{}, interrupted
		}
		if c.now().Sub(started) >= c.whole {
			return 0, qcow2Info{}, c.stillStreaming(rawURL, count, declared)
		}
		if errors.Is(readErr, io.ErrUnexpectedEOF) {
			return 0, qcow2Info{}, verifyFailed("%s sent %d of its declared %d bytes", rawURL, count, declared)
		}
		return 0, qcow2Info{}, verifyFailed("%s failed after %d of %d bytes: %v", rawURL, count, declared, readErr)
	}
	if count != declared {
		return 0, qcow2Info{}, verifyFailed("%s sent %d of its declared %d bytes", rawURL, count, declared)
	}
	if info == nil {
		parsed, err := parseQCOW2Header(head)
		if err != nil {
			return 0, qcow2Info{}, err
		}
		info = &parsed
	}
	have := hex.EncodeToString(digest.Sum(nil))
	if have != expectedSHA256 {
		return 0, qcow2Info{}, verifyFailed("%s served sha256 %s, not the %s the lane names", rawURL, have, expectedSHA256)
	}
	return count, *info, nil
}

func asciiDigits(text string) bool {
	if text == "" {
		return false
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// check fetches the URL and describes what it served.
func (c *imageChecker) check(ctx context.Context, slot, rawURL, expectedSHA256 string) (map[string]any, error) {
	if !imageURLPattern.MatchString(rawURL) {
		return nil, unsupported("%s", imageURLMessage)
	}
	probe, accept := c.probeRange(ctx, rawURL)
	size, info, err := c.stream(ctx, rawURL, expectedSHA256)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"schema":            nodeImageSchema,
		"slot":              slot,
		"method":            methodGet,
		"observedByThisRun": true,
		"url":               rawURL,
		"sha256":            expectedSHA256,
		"bytes":             size,
		"rangeProbe":        probe,
		"rangeSupported":    probe == "honored",
		"acceptRanges":      accept,
		"qcow2":             info.record(),
	}, nil
}

// verification is how the outputs manifest says a node image was checked.
type verification struct {
	method       string
	run          string
	bytes        int64
	virtualBytes int64
	qcow2Version int
}

// whole reports a positive integer. A JSON true, a string and 2.0 must not pass for 1, "2" and 2.
func whole(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok || !integerPattern.MatchString(number.String()) {
		return 0, false
	}
	parsed, err := strconv.ParseInt(number.String(), 10, 64)
	return parsed, err == nil && parsed >= 1
}

// parseVerification returns a node image's verification from the outputs manifest, or refuses one the lane does
// not know.
func parseVerification(value any) (verification, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return verification{}, unsupported("a node image's verification must be an object")
	}
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	method, _ := object["method"].(string)
	switch method {
	case methodGet:
		if strings.Join(keys, ",") != "method" {
			return verification{}, unsupported("verification %s takes no other field, got %v", methodGet, keys)
		}
		return verification{method: methodGet}, nil
	case methodReadback:
		if strings.Join(keys, ",") != strings.Join(readbackKeys, ",") {
			return verification{}, unsupported("verification %s needs exactly %v, got %v", methodReadback, readbackKeys, keys)
		}
		run, isText := object["run"].(string)
		if !isText || !runURLPattern.MatchString(run) {
			return verification{}, unsupported("verification run must be the https URL of a GitHub Actions run")
		}
		size, sizeOK := whole(object["bytes"])
		virtual, virtualOK := whole(object["virtualSizeBytes"])
		if !sizeOK || !virtualOK {
			return verification{}, unsupported("verification bytes and virtualSizeBytes must be positive integers")
		}
		version, versionOK := whole(object["qcow2Version"])
		if !versionOK || (version != 2 && version != 3) {
			return verification{}, unsupported("verification qcow2Version must be 2 or 3")
		}
		return verification{method: methodReadback, run: run, bytes: size, virtualBytes: virtual, qcow2Version: int(version)}, nil
	}
	return verification{}, unsupported("verification method must be %s or %s, got %s", methodGet, methodReadback, describeJSON(object["method"]))
}

// describeJSON renders a decoded value as the JSON it was written as, so a message can say what it found: null
// for a missing field, 5 for a number and a quoted string for text.
func describeJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(data)
}

// recorded describes what the publication run reported, without fetching anything.
func recorded(slot, rawURL, expectedSHA256 string, v verification) (map[string]any, error) {
	if !imageURLPattern.MatchString(rawURL) {
		return nil, unsupported("%s", imageURLMessage)
	}
	return map[string]any{
		"schema":            nodeImageSchema,
		"slot":              slot,
		"method":            methodReadback,
		"observedByThisRun": false,
		"run":               v.run,
		"url":               rawURL,
		"sha256":            expectedSHA256,
		"bytes":             v.bytes,
		"qcow2":             map[string]any{"version": v.qcow2Version, "virtualSizeBytes": v.virtualBytes},
	}, nil
}

// decodeJSON reads exactly one JSON value, keeping numbers as written so integers can be told from floats.
func decodeJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("extra data after the JSON value")
	}
	return value, nil
}

// namedIn returns the URL, digest and verification the substituted plan's record names for one node image.
func namedIn(path, slot string) (rawURL, digest string, v verification, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", verification{}, unsupported("%s is not readable JSON: %v", path, err)
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxVerificationBytes+1))
	if err != nil {
		return "", "", verification{}, unsupported("%s is not readable JSON: %v", path, err)
	}
	if len(data) > maxVerificationBytes {
		return "", "", verification{}, unsupported("%s exceeds %d bytes", path, maxVerificationBytes)
	}
	// The decoder would replace bytes that are not UTF-8 and read on, so the record must be text before it is read.
	if !utf8.Valid(data) {
		return "", "", verification{}, unsupported("%s is not readable JSON: it is not valid UTF-8", path)
	}
	decoded, err := decodeJSON(data)
	if err != nil {
		return "", "", verification{}, unsupported("%s is not readable JSON: %v", path, err)
	}
	record, isObject := decoded.(map[string]any)
	if !isObject || record["schema"] != substitutionsSchema {
		return "", "", verification{}, unsupported("%s is not a %s record", path, substitutionsSchema)
	}
	if record["artifactSource"] != "nvkr-outputs" {
		return "", "", verification{}, unsupported("only a run of NVKR outputs names node images to check")
	}
	var found []map[string]any
	if replaced, isList := record["replaced"].([]any); isList {
		for _, item := range replaced {
			entry, isEntry := item.(map[string]any)
			if !isEntry {
				return "", "", verification{}, unsupported("%s lists a replacement that is not an object", path)
			}
			if entry["slot"] == slot {
				found = append(found, entry)
			}
		}
	}
	if len(found) != 1 {
		return "", "", verification{}, unsupported("%s names %d replacements for %s, want exactly one", path, len(found), slot)
	}
	rawURL, urlOK := found[0]["to"].(string)
	digest, digestOK := found[0]["sha256"].(string)
	if !urlOK || !digestOK || !sha256Pattern.MatchString(digest) {
		return "", "", verification{}, unsupported("the replacement for %s names no URL and lower-case sha256", slot)
	}
	v, err = parseVerification(found[0]["verification"])
	if err != nil {
		return "", "", verification{}, err
	}
	return rawURL, digest, v, nil
}

// positiveFlag defines an integer flag that must be positive.
func positiveFlag(set *flagSet, name string, def int64, usage string) *int64 {
	value := new(int64)
	*value = def
	set.fs.Func(name, fmt.Sprintf("%s (default %d)", usage, def), func(text string) error {
		parsed, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return fmt.Errorf("%q is not an integer", text)
		}
		if parsed <= 0 {
			return errors.New("must be positive")
		}
		*value = parsed
		return nil
	})
	return value
}

func runCheckNodeImage(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return checkNodeImage(ctx, args, stdout, stderr, httpFetcher{}, time.Now)
}

func checkNodeImage(ctx context.Context, args []string, stdout, stderr io.Writer, fetch fetcher, now func() time.Time) error {
	set := newCommandFlags("check-node-image")
	slot := set.choice("slot", "the node image slot to check", nodeImageSlots)
	rawURL := set.fs.String("url", "", "fetch this URL; with --sha256, instead of --substitutions")
	digest := new(string)
	set.fs.Func("sha256", "the digest the URL must serve, 64 lower-case hex digits", func(text string) error {
		if !sha256Pattern.MatchString(text) {
			return errors.New("must be 64 lower-case hex digits")
		}
		*digest = text
		return nil
	})
	substitutions := set.fs.String("substitutions", "", "take the URL, digest and method from the substitution record")
	out := set.fs.String("out", "", "record to create; must not exist")
	timeout := positiveFlag(set, "timeout", defaultTimeoutSecs, "seconds per read")
	maxSeconds := positiveFlag(set, "max-seconds", defaultMaxSecs, "seconds for the whole body")
	maxBytes := positiveFlag(set, "max-bytes", defaultMaxBytes, "the largest body to accept")
	if err := set.parse(args, stderr, checkNodeImageUsage); err != nil {
		return err
	}
	given := set.given()
	if err := requireFlags(given, "slot", "out"); err != nil {
		return err
	}
	if given["substitutions"] {
		if given["url"] || given["sha256"] {
			return usageError("--substitutions names the URL and digest; do not also pass --url or --sha256")
		}
	} else if !given["url"] || !given["sha256"] {
		return usageError("give --substitutions, or both --url and --sha256")
	}
	if pathExists(*out) {
		return immutable("refusing to replace %s", *out)
	}
	imageURL, imageDigest, method := *rawURL, *digest, verification{method: methodGet}
	if given["substitutions"] {
		var err error
		imageURL, imageDigest, method, err = namedIn(*substitutions, *slot)
		if err != nil {
			return err
		}
	}
	var record map[string]any
	var err error
	if method.method == methodGet {
		checker := newImageChecker(fetch, now, time.Duration(*timeout)*time.Second, time.Duration(*maxSeconds)*time.Second, *maxBytes)
		record, err = checker.check(ctx, *slot, imageURL, imageDigest)
	} else {
		record, err = recorded(*slot, imageURL, imageDigest, method)
	}
	if err != nil {
		return err
	}
	data, err := marshalRecord(record)
	if err != nil {
		return nerrors.Wrap(nerrors.CodeInternal, err, "encode the node image record")
	}
	if err := writeNewFile(*out, data, 0o600); err != nil {
		return err
	}
	if record["method"] == methodGet {
		fmt.Fprintf(stdout, "nvkr: node image ok: %s, %d bytes, sha256 %s, range %s\n", *slot, record["bytes"], record["sha256"], record["rangeProbe"])
	} else {
		fmt.Fprintf(stdout, "nvkr: node image recorded, not fetched: %s, %d bytes, sha256 %s, read back by %s\n", *slot, record["bytes"], record["sha256"], record["run"])
	}
	return nil
}
