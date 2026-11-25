// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

// publish-node-image: copy an NVKR-built node image to the HTTPS location the hardware site's imager reads, and
// record what was checked.
//
// The hardware site boots a node from a plain HTTPS URL, and the registry that holds NVKR's publication needs a
// token the imager cannot present. This is the workflow's stand-in for NVKR's own boot-asset publication
// (ADR-20): it pulls one disk output by exact digest, checks that it is a plain qcow2, copies it to an object
// store under a key that contains its sha256, reads it back from the store with credentials and hashes what it
// read. The record it writes is a description. It is unsigned, binds no projection of the publication and
// carries qualificationAuthority "none".
//
// The read-back goes to the object store, never through the HTTPS front end: that front end can sit behind an
// address allowlist that CI runners are not on, so a request from here proves nothing about the imager's path.
// Whether the imager can fetch the URL, trusts its certificate and boots the image is established only by a
// machine booting it.
//
// The key contains the sha256, so a second publication of the same image rewrites identical bytes and a
// different image can never replace this one. An object already at the key must read back to the same bytes, or
// the command stops with exit 47 and changes nothing. The destination prefix must begin with nvkr/, so a
// misspelled prefix cannot land in a tree another producer owns.
//
// Every child process (the pull and each object store call) runs as an owned process group under its own
// deadline, so no tool outlives the command and an interrupted run leaves nothing running.
//
// Exit codes: 0 ok, 2 usage, 13 unsupported image or destination, 17 a precondition failed, 20 the pull failed,
// 22 the bytes did not match, 30 the object store failed, 31 a deadline elapsed, 47 an existing object or output
// differs.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	nerrors "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const (
	publicationSchema   = "nvkr.dev/hardware-e2e-node-image-publication/v1"
	pullResultSchema    = "nvkr.dev/release-pull-result/v1"
	publishNodeUsage    = "--slot SLOT --reference REF --output NAME --version VERSION --s3-uri URI --prefix PREFIX --cdn-domain HOST --region REGION --work-dir DIR --out DIR [--run-url URL] [--nvkr PATH] [--aws PATH] [--dry-run]"
	maxCapturedBytes    = 16 << 20
	maxCapturedStderr   = 64 << 10
	pullTimeout         = 35 * time.Minute
	pullTimeoutSlack    = time.Minute
	defaultStoreTimeout = time.Hour
	defaultShortTimeout = 2 * time.Minute
)

var (
	slotArch         = map[string]string{"os-image-amd64": "amd64", "os-image-arm64": "arm64"}
	publishSlots     = []string{"os-image-amd64", "os-image-arm64"}
	referencePattern = regexp.MustCompile(`^oci://([a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]+)?(/[a-z0-9._-]+)+@sha256:[0-9a-f]{64})$`)
	outputPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	versionPattern   = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?$`)
	s3URIPattern     = regexp.MustCompile(`^s3://([a-z0-9][a-z0-9.-]{1,61}[a-z0-9])((?:/[A-Za-z0-9][A-Za-z0-9._-]*)*)$`)
	prefixPattern    = regexp.MustCompile(`^nvkr(/[A-Za-z0-9][A-Za-z0-9._-]*)*$`)
	domainPattern    = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]+)?$`)
	regionPattern    = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]+$`)
)

func preflight(format string, args ...any) error {
	return nerrors.New(nerrors.CodePreflightFailed, format, args...)
}

func fetchFailed(format string, args ...any) error {
	return nerrors.New(nerrors.CodeFetchFailed, format, args...)
}

func storeFailed(format string, args ...any) error {
	return nerrors.New(nerrors.CodeUnavailable, format, args...)
}

func timedOut(format string, args ...any) error {
	return nerrors.New(nerrors.CodeTimeout, format, args...)
}

// publishLimits are the deadlines of the child processes. Tests shorten them; the command never does.
type publishLimits struct {
	pull  time.Duration
	store time.Duration
	short time.Duration
}

var defaultPublishLimits = publishLimits{pull: pullTimeout, store: defaultStoreTimeout, short: defaultShortTimeout}

// publishConfig is the validated request. Nothing is built from a value that did not match its pattern.
type publishConfig struct {
	slot              string
	arch              string
	reference         string
	registryReference string
	output            string
	version           string
	bucket            string
	prefix            string
	cdnDomain         string
	region            string
	runURL            string
	dryRun            bool
	workDir           string
	out               string
	nvkr              string
	aws               string
	keyTemplate       string
	limits            publishLimits
}

type publishArgs struct {
	slot, reference, output, version, s3URI, prefix, cdnDomain, region string
	workDir, out, runURL, nvkr, aws                                    string
	dryRun                                                             bool
}

func newPublishConfig(a publishArgs, limits publishLimits) (*publishConfig, error) {
	cfg := &publishConfig{slot: a.slot, arch: slotArch[a.slot], limits: limits}
	match := referencePattern.FindStringSubmatch(a.reference)
	if match == nil {
		return nil, unsupported("the reference must be oci://REPOSITORY@sha256:DIGEST, pinned by digest and not by tag")
	}
	cfg.reference, cfg.registryReference = a.reference, match[1]
	if !outputPattern.MatchString(a.output) {
		return nil, unsupported("the output must be a plain output name")
	}
	cfg.output = a.output
	if !versionPattern.MatchString(a.version) {
		return nil, unsupported("the version must look like vX.Y.Z or vX.Y.Z-pre")
	}
	cfg.version = a.version
	store := s3URIPattern.FindStringSubmatch(a.s3URI)
	if store == nil {
		return nil, preflight("the object store root must be s3://BUCKET[/PATH] with no trailing slash")
	}
	cfg.bucket = store[1]
	root := strings.Trim(store[2], "/")
	if !prefixPattern.MatchString(a.prefix) {
		return nil, unsupported("the prefix must begin with nvkr/ and hold plain path segments")
	}
	cfg.prefix = a.prefix
	if !domainPattern.MatchString(a.cdnDomain) {
		return nil, preflight("the HTTPS domain must be a lower-case host name")
	}
	cfg.cdnDomain = a.cdnDomain
	if !regionPattern.MatchString(a.region) {
		return nil, preflight("the region must look like us-west-2")
	}
	cfg.region = a.region
	if !a.dryRun {
		if a.runURL == "" || !runURLPattern.MatchString(a.runURL) {
			return nil, unsupported("a publication names the workflow run that made it: --run-url https://github.com/OWNER/REPO/actions/runs/ID")
		}
	} else if a.runURL != "" && !runURLPattern.MatchString(a.runURL) {
		return nil, unsupported("--run-url must be https://github.com/OWNER/REPO/actions/runs/ID")
	}
	cfg.runURL, cfg.dryRun = a.runURL, a.dryRun
	cfg.workDir, cfg.out, cfg.nvkr, cfg.aws = a.workDir, a.out, a.nvkr, a.aws
	var parts []string
	for _, part := range []string{root, cfg.prefix, cfg.version, cfg.arch, "{sha256}", cfg.output + ".qcow2"} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	cfg.keyTemplate = strings.Join(parts, "/")
	return cfg, nil
}

func (c *publishConfig) key(digest string) string {
	return strings.Replace(c.keyTemplate, "{sha256}", digest, 1)
}

func (c *publishConfig) url(digest string) (string, error) {
	location := "https://" + c.cdnDomain + "/" + c.key(digest)
	if !imageURLPattern.MatchString(location) {
		return "", unsupported("the object key does not make a plain https URL to a .qcow2")
	}
	return location, nil
}

// cappedBuffer keeps the start of what a child writes and counts the rest, so a noisy tool cannot fill memory.
type cappedBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if room := c.limit - c.buffer.Len(); room > 0 {
		c.buffer.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func (c *cappedBuffer) text() string {
	return strings.TrimSpace(strings.ToValidUTF8(c.buffer.String(), "\uFFFD"))
}

// hashingWriter hashes and counts what a child writes without keeping it.
type hashingWriter struct {
	digest hash.Hash
	count  int64
}

func (h *hashingWriter) Write(p []byte) (int, error) {
	h.digest.Write(p)
	h.count += int64(len(p))
	return len(p), nil
}

func toolEnv() []string {
	return append(os.Environ(), "AWS_PAGER=")
}

// tools runs the child processes this command depends on, each as an owned group under its own deadline.
type tools struct {
	limits publishLimits
}

// classify turns the end of an owned child into a coded failure, or into its exit status when it ran and failed.
func classify(parent context.Context, argv []string, timeout time.Duration, err error, what string) (exitCode int, failure error) {
	if err == nil {
		return 0, nil
	}
	if parent.Err() != nil {
		return 0, timedOut("%s was interrupted", what)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return 0, timedOut("%s was still running after %gs", what, timeout.Seconds())
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode()
		if code == 0 {
			code = -1
		}
		return code, nil
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return 0, preflight("%s is not installed", argv[0])
	}
	return 0, nerrors.Wrap(nerrors.CodeInternal, err, "run %s", argv[0])
}

func describe(argv []string) string {
	if len(argv) > 1 {
		return argv[0] + " " + argv[1]
	}
	return argv[0] + " "
}

// capture runs argv and returns its exit status, standard output and trimmed standard error.
func (t tools) capture(parent context.Context, argv []string, timeout time.Duration, stdin []byte) (int, []byte, string, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = toolEnv()
	stdout, stderr := &cappedBuffer{limit: maxCapturedBytes}, &cappedBuffer{limit: maxCapturedStderr}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	err := ownedcmd.Run(ctx, cmd)
	code, failure := classify(parent, argv, timeout, err, describe(argv))
	if failure != nil {
		return 0, nil, "", failure
	}
	return code, stdout.buffer.Bytes(), stderr.text(), nil
}

// streamSHA256 hashes what a command writes to standard output without keeping it.
func (t tools) streamSHA256(parent context.Context, argv []string, timeout time.Duration) (string, int64, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = toolEnv()
	hasher, stderr := &hashingWriter{digest: sha256.New()}, &cappedBuffer{limit: maxCapturedStderr}
	cmd.Stdout, cmd.Stderr = hasher, stderr
	err := ownedcmd.Run(ctx, cmd)
	code, failure := classify(parent, argv, timeout, err, "reading back from the object store")
	if failure != nil {
		return "", 0, failure
	}
	if code != 0 {
		return "", 0, storeFailed("%s exited %d after %d bytes: %s", argv[0], code, hasher.count, stderr.text())
	}
	return hex.EncodeToString(hasher.digest.Sum(nil)), hasher.count, nil
}

// pulledImage is what the pull reported for the one disk output, before the bytes are checked.
type pulledImage struct {
	path         string
	sha256       string
	size         int64
	declaredArch string
	digest       any
	buildDigest  any
}

func pull(ctx context.Context, cfg *publishConfig, runner tools) (*pulledImage, error) {
	if pathExists(cfg.workDir) {
		return nil, preflight("%s must not exist and its parent must", cfg.workDir)
	}
	if info, err := os.Stat(filepath.Dir(cfg.workDir)); err != nil || !info.IsDir() {
		return nil, preflight("%s must not exist and its parent must", cfg.workDir)
	}
	argv := []string{cfg.nvkr, "release", "pull", "--reference", cfg.registryReference, "--output", cfg.output,
		"--out", cfg.workDir, "--format", "json", "--timeout", strconv.Itoa(int(cfg.limits.pull/time.Minute)) + "m"}
	code, stdout, stderr, err := runner.capture(ctx, argv, cfg.limits.pull+pullTimeoutSlack, nil)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fetchFailed("release pull exited %d: %s", code, stderr)
	}
	decoded, err := decodeJSON(stdout)
	if err != nil {
		return nil, verifyFailed("release pull printed no result this script understands: %v", err)
	}
	result, isObject := decoded.(map[string]any)
	if !isObject {
		return nil, verifyFailed("release pull printed no result this script understands: the result is not an object")
	}
	output, hasOutput := result["output"].(map[string]any)
	if !hasOutput {
		return nil, verifyFailed("release pull printed no result this script understands: no output object")
	}
	files, hasFiles := result["files"]
	if !hasFiles {
		return nil, verifyFailed("release pull printed no result this script understands: no files")
	}
	if result["schemaVersion"] != pullResultSchema {
		return nil, verifyFailed("release pull result has schema %s, not %s", describeJSON(result["schemaVersion"]), pullResultSchema)
	}
	if result["reference"] != cfg.registryReference || output["name"] != cfg.output {
		return nil, verifyFailed("release pull answered for a different reference or output than the one asked")
	}
	if output["kind"] != "disk-image" {
		return nil, unsupported("output %s is a %s, not a disk-image", cfg.output, describeJSON(output["kind"]))
	}
	// A published disk image always names its architecture, taken from its verified wrapper. An output that names
	// none is not one this lane can place in a slot: a wrong-architecture image would boot nothing.
	declared, named := output["arch"]
	if !named || declared == nil {
		return nil, verifyFailed("output %s declares no architecture, so it cannot be placed in the slot %s (%s)", cfg.output, cfg.slot, cfg.arch)
	}
	if declared != cfg.arch {
		return nil, verifyFailed("output %s is built for %s, but the slot %s is %s", cfg.output, describeJSON(declared), cfg.slot, cfg.arch)
	}
	list, isList := files.([]any)
	var entry map[string]any
	if isList && len(list) == 1 {
		entry, _ = list[0].(map[string]any)
	}
	if entry == nil || entry["path"] != cfg.output+".qcow2" {
		count := "no"
		if isList {
			count = strconv.Itoa(len(list))
		}
		return nil, verifyFailed("release pull wrote %s files; expected only %s.qcow2", count, cfg.output)
	}
	digest, digestIsText := entry["sha256"].(string)
	size, sizeOK := whole(entry["size"])
	if !digestIsText || !sha256Pattern.MatchString(digest) || !sizeOK {
		return nil, verifyFailed("release pull gave no usable sha256 and size for the image")
	}
	return &pulledImage{
		path:         filepath.Join(cfg.workDir, cfg.output+".qcow2"),
		sha256:       digest,
		size:         size,
		declaredArch: cfg.arch,
		digest:       output["digest"],
		buildDigest:  output["buildDigest"],
	}, nil
}

// inspectImage hashes the file as it is on disk and requires it to be the bytes the pull reported, as a plain
// qcow2.
func inspectImage(path, wantSHA256 string, wantSize int64) (qcow2Info, error) {
	linkInfo, err := os.Lstat(path)
	if err != nil {
		return qcow2Info{}, verifyFailed("the pulled image could not be opened: %v", err)
	}
	if !linkInfo.Mode().IsRegular() {
		return qcow2Info{}, verifyFailed("the pulled image is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return qcow2Info{}, verifyFailed("the pulled image could not be opened: %v", err)
	}
	defer func() { _ = file.Close() }()
	// The pull has exited, so only another process on the runner can replace the path between the Lstat above
	// and the open. The Lstat comes first so that a FIFO is refused before open can block on it. No test
	// reaches the comparison below, because it differs only when that replacement races these two calls.
	openInfo, err := file.Stat()
	if err != nil || !os.SameFile(linkInfo, openInfo) {
		return qcow2Info{}, verifyFailed("the pulled image changed while it was opened")
	}
	digest := sha256.New()
	var count int64
	var head []byte
	var info *qcow2Info
	buffer := make([]byte, chunkBytes)
	for {
		n, readErr := file.Read(buffer)
		if n > 0 {
			count += int64(n)
			if count > defaultMaxBytes {
				return qcow2Info{}, unsupported("the image is over the %d byte limit", defaultMaxBytes)
			}
			digest.Write(buffer[:n])
			if info == nil {
				head = append(head, buffer[:n]...)
				if len(head) >= qcow2HeaderLength(head) {
					parsed, err := parseQCOW2Header(head)
					if err != nil {
						return qcow2Info{}, err
					}
					info = &parsed
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return qcow2Info{}, verifyFailed("the pulled image could not be read: %v", readErr)
		}
	}
	if info == nil {
		parsed, err := parseQCOW2Header(head)
		if err != nil {
			return qcow2Info{}, err
		}
		info = &parsed
	}
	if count != wantSize {
		return qcow2Info{}, verifyFailed("the pulled image is %d bytes; release pull reported %d", count, wantSize)
	}
	if have := hex.EncodeToString(digest.Sum(nil)); have != wantSHA256 {
		return qcow2Info{}, verifyFailed("the pulled image hashes to %s; release pull reported %s", have, wantSHA256)
	}
	return *info, nil
}

// objectStore is the few object store calls this command makes, each bounded and each coded on failure.
type objectStore struct {
	cfg    *publishConfig
	runner tools
}

func (s objectStore) command(args ...string) []string {
	argv := append([]string{s.cfg.aws}, args...)
	return append(argv, "--region", s.cfg.region)
}

// head returns the length of the object at key, and whether there is one.
func (s objectStore) head(ctx context.Context, key string) (int64, bool, error) {
	argv := s.command("s3api", "head-object", "--bucket", s.cfg.bucket, "--key", key, "--output", "json")
	code, stdout, stderr, err := s.runner.capture(ctx, argv, s.cfg.limits.short, nil)
	if err != nil {
		return 0, false, err
	}
	if code == 0 {
		decoded, err := decodeJSON(stdout)
		var length int64
		var ok bool
		if err == nil {
			if object, isObject := decoded.(map[string]any); isObject {
				length, ok = parseContentLength(object["ContentLength"])
			}
		}
		if !ok {
			return 0, false, storeFailed("head-object for %s answered something this script does not understand", key)
		}
		return length, true, nil
	}
	if strings.Contains(stderr, "(404)") || strings.Contains(stderr, "Not Found") {
		return 0, false, nil
	}
	hint := ""
	if strings.Contains(stderr, "(403)") || strings.Contains(stderr, "Forbidden") {
		hint = " (S3 also answers 403 for a missing key when the role cannot list the prefix, so the role needs s3:ListBucket on it)"
	}
	return 0, false, storeFailed("head-object for %s failed: %s%s", key, stderr, hint)
}

// parseContentLength reads a non-negative integer ContentLength.
func parseContentLength(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok || !integerPattern.MatchString(number.String()) {
		return 0, false
	}
	parsed, err := strconv.ParseInt(number.String(), 10, 64)
	return parsed, err == nil && parsed >= 0
}

func (s objectStore) putFile(ctx context.Context, path, key string) error {
	argv := s.command("s3", "cp", path, "s3://"+s.cfg.bucket+"/"+key, "--only-show-errors")
	code, _, stderr, err := s.runner.capture(ctx, argv, s.cfg.limits.store, nil)
	if err != nil {
		return err
	}
	if code != 0 {
		return storeFailed("uploading %s failed: %s", key, stderr)
	}
	return nil
}

func (s objectStore) putText(ctx context.Context, text, key string) error {
	argv := s.command("s3", "cp", "-", "s3://"+s.cfg.bucket+"/"+key, "--only-show-errors")
	code, _, stderr, err := s.runner.capture(ctx, argv, s.cfg.limits.short, []byte(text))
	if err != nil {
		return err
	}
	if code != 0 {
		return storeFailed("uploading %s failed: %s", key, stderr)
	}
	return nil
}

func (s objectStore) readText(ctx context.Context, key string) (string, error) {
	argv := s.command("s3", "cp", "s3://"+s.cfg.bucket+"/"+key, "-", "--only-show-errors")
	code, stdout, stderr, err := s.runner.capture(ctx, argv, s.cfg.limits.short, nil)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", storeFailed("reading %s failed: %s", key, stderr)
	}
	return strings.ToValidUTF8(string(stdout), "\uFFFD"), nil
}

func (s objectStore) readBack(ctx context.Context, key string) (string, int64, error) {
	argv := s.command("s3", "cp", "s3://"+s.cfg.bucket+"/"+key, "-", "--only-show-errors")
	return s.runner.streamSHA256(ctx, argv, s.cfg.limits.store)
}

// published is what a publication did.
type published struct {
	reused        bool
	sidecarReused bool
	contentLength int64
}

// publish puts the image at its key, then requires the store to hold exactly these bytes.
func publish(ctx context.Context, cfg *publishConfig, image *pulledImage, store objectStore) (published, error) {
	key := cfg.key(image.sha256)
	sidecar := key + ".sha256"
	_, existed, err := store.head(ctx, key)
	if err != nil {
		return published{}, err
	}
	if !existed {
		if err := store.putFile(ctx, image.path, key); err != nil {
			return published{}, err
		}
	}
	mismatch := verifyFailed
	if existed {
		mismatch = immutable
	}
	length, found, err := store.head(ctx, key)
	if err != nil {
		return published{}, err
	}
	if !found {
		return published{}, mismatch("%s is missing from the object store; the image is %d bytes", key, image.size)
	}
	if length != image.size {
		return published{}, mismatch("%s is %d bytes in the object store; the image is %d", key, length, image.size)
	}
	have, count, err := store.readBack(ctx, key)
	if err != nil {
		return published{}, err
	}
	if have != image.sha256 || count != image.size {
		return published{}, mismatch("%s reads back as sha256 %s (%d bytes), not %s (%d bytes)", key, have, count, image.sha256, image.size)
	}
	_, sidecarExisted, err := store.head(ctx, sidecar)
	if err != nil {
		return published{}, err
	}
	if !sidecarExisted {
		if err := store.putText(ctx, image.sha256, sidecar); err != nil {
			return published{}, err
		}
	}
	text, err := store.readText(ctx, sidecar)
	if err != nil {
		return published{}, err
	}
	if text != image.sha256 {
		return published{}, immutable("%s holds %q, not %s", sidecar, text, image.sha256)
	}
	return published{reused: existed, sidecarReused: sidecarExisted, contentLength: length}, nil
}

func recordFor(cfg *publishConfig, image *pulledImage, info qcow2Info, done published) (map[string]any, error) {
	key := cfg.key(image.sha256)
	location, err := cfg.url(image.sha256)
	if err != nil {
		return nil, err
	}
	source := func() map[string]any {
		return map[string]any{"kind": "nvkr-release", "reference": cfg.reference, "version": cfg.version}
	}
	return map[string]any{
		"schema":                 publicationSchema,
		"slot":                   cfg.slot,
		"qualificationAuthority": "none",
		"signedProjection":       false,
		"source":                 source(),
		"pulled": map[string]any{
			"output": cfg.output, "declaredArch": image.declaredArch, "digest": image.digest, "buildDigest": image.buildDigest,
		},
		"store": map[string]any{
			"bucket": cfg.bucket, "key": key, "sidecarKey": key + ".sha256", "region": cfg.region,
			"contentLength": done.contentLength, "reusedExistingObject": done.reused, "reusedExistingSidecar": done.sidecarReused,
		},
		"qcow2": info.record(),
		"manifestEntry": map[string]any{
			"url":    location,
			"sha256": image.sha256,
			"source": source(),
			"verification": map[string]any{
				"method": methodReadback, "run": cfg.runURL, "bytes": image.size,
				"qcow2Version": info.version, "virtualSizeBytes": info.virtualSize,
			},
		},
	}, nil
}

// writeRecord creates the record directory and its one file, privately and exclusively.
func writeRecord(out, slot string, record map[string]any) error {
	if pathExists(out) {
		return immutable("refusing to replace %s", out)
	}
	if err := os.Mkdir(out, 0o700); err != nil {
		return immutable("could not create the record under %s: %v", out, err)
	}
	data, err := marshalRecord(record)
	if err != nil {
		return nerrors.Wrap(nerrors.CodeInternal, err, "encode the publication record")
	}
	if err := writeNewFile(filepath.Join(out, slot+".json"), data, 0o600); err != nil {
		return immutable("could not create the record under %s: %v", out, err)
	}
	return nil
}

func runPublishNodeImage(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return publishNodeImage(ctx, args, stdout, stderr, defaultPublishLimits)
}

func publishNodeImage(ctx context.Context, args []string, stdout, stderr io.Writer, limits publishLimits) error {
	set := newCommandFlags("publish-node-image")
	slot := set.choice("slot", "the node image slot", publishSlots)
	reference := set.fs.String("reference", "", "oci://REPOSITORY@sha256:DIGEST of the publication root")
	output := set.fs.String("output", "", "the exact disk output name")
	version := set.fs.String("version", "", "the NVKE version the image was built at")
	s3URI := set.fs.String("s3-uri", "", "s3://BUCKET[/PATH], the object store root")
	prefix := set.fs.String("prefix", "", "a path under the root; begins with nvkr/")
	cdnDomain := set.fs.String("cdn-domain", "", "the host that serves the root over HTTPS")
	region := set.fs.String("region", "", "the object store region")
	workDir := set.fs.String("work-dir", "", "a new directory that receives the pulled image")
	out := set.fs.String("out", "", "a new directory that receives the publication record")
	runURL := set.fs.String("run-url", "", "the workflow run that publishes; required unless --dry-run")
	nvkrTool := set.fs.String("nvkr", "nvkr", "the nvkr executable")
	awsTool := set.fs.String("aws", "aws", "the aws executable")
	dryRun := set.fs.Bool("dry-run", false, "pull and check the image and name its location; write nothing")
	if err := set.parse(args, stderr, publishNodeUsage); err != nil {
		return err
	}
	given := set.given()
	if err := requireFlags(given, "slot", "reference", "output", "version", "s3-uri", "prefix", "cdn-domain", "region", "work-dir", "out"); err != nil {
		return err
	}
	cfg, err := newPublishConfig(publishArgs{
		slot: *slot, reference: *reference, output: *output, version: *version, s3URI: *s3URI, prefix: *prefix,
		cdnDomain: *cdnDomain, region: *region, workDir: *workDir, out: *out, runURL: *runURL,
		nvkr: *nvkrTool, aws: *awsTool, dryRun: *dryRun,
	}, limits)
	if err != nil {
		return err
	}
	required := []string{cfg.nvkr}
	if !cfg.dryRun {
		required = append(required, cfg.aws)
	}
	for _, tool := range required {
		if _, err := exec.LookPath(tool); err != nil {
			return preflight("%s is not installed", tool)
		}
	}
	if !cfg.dryRun && pathExists(cfg.out) {
		return immutable("refusing to replace %s", cfg.out)
	}
	runner := tools{limits: limits}
	image, err := pull(ctx, cfg, runner)
	if err != nil {
		return err
	}
	info, err := inspectImage(image.path, image.sha256, image.size)
	if err != nil {
		return err
	}
	// The location is named, and shown to be a plain https URL to a .qcow2, before anything is written.
	key := cfg.key(image.sha256)
	location, err := cfg.url(image.sha256)
	if err != nil {
		return err
	}
	if cfg.dryRun {
		data, err := marshalRecord(map[string]any{
			"dryRun": true, "slot": cfg.slot, "bytes": image.size, "sha256": image.sha256,
			"key": key, "url": location, "qcow2": info.record(),
		})
		if err != nil {
			return nerrors.Wrap(nerrors.CodeInternal, err, "encode the dry run report")
		}
		_, err = stdout.Write(data)
		return err
	}
	done, err := publish(ctx, cfg, image, objectStore{cfg: cfg, runner: runner})
	if err != nil {
		return err
	}
	record, err := recordFor(cfg, image, info, done)
	if err != nil {
		return err
	}
	if err := writeRecord(cfg.out, cfg.slot, record); err != nil {
		return err
	}
	how := "uploaded"
	if done.reused {
		how = "reused the existing object"
	}
	fmt.Fprintf(stdout, "nvkr: node image published: %s, %d bytes, sha256 %s, %s; read back from s3://%s/%s\n",
		cfg.slot, image.size, image.sha256, how, cfg.bucket, key)
	return nil
}
