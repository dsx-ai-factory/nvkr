// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// qcow2Spec describes a header to build. The zero value is not valid; start from newQCOW2.
type qcow2Spec struct {
	version      uint32
	backing      uint64
	bits         uint32
	size         uint64
	crypt        uint32
	magic        []byte
	incompatible uint64
}

func newQCOW2() qcow2Spec {
	return qcow2Spec{version: 3, bits: 16, size: 12 << 30, magic: []byte{'Q', 'F', 'I', 0xfb}}
}

func (s qcow2Spec) with(edit func(*qcow2Spec)) qcow2Spec {
	edit(&s)
	return s
}

// header builds the base header, and for version 3 the feature fields after it.
func (s qcow2Spec) header() []byte {
	head := make([]byte, qcow2HeaderBytes)
	copy(head[0:4], s.magic)
	binary.BigEndian.PutUint32(head[4:8], s.version)
	binary.BigEndian.PutUint64(head[8:16], s.backing)
	binary.BigEndian.PutUint32(head[20:24], s.bits)
	binary.BigEndian.PutUint64(head[24:32], s.size)
	binary.BigEndian.PutUint32(head[32:36], s.crypt)
	if s.version == 3 {
		extension := make([]byte, qcow2V3HeaderBytes-qcow2HeaderBytes)
		binary.BigEndian.PutUint64(extension[0:8], s.incompatible)
		binary.BigEndian.PutUint32(extension[24:28], 4)
		binary.BigEndian.PutUint32(extension[28:32], qcow2V3HeaderBytes)
		head = append(head, extension...)
	}
	return head
}

func (s qcow2Spec) image(payload []byte) []byte {
	return append(s.header(), payload...)
}

func payloadOf(text string, times int) []byte {
	return bytes.Repeat([]byte(text), times)
}

func sumHex(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

// chunkBody is a response body that hands out its chunks one Read at a time, so a test controls where the reads
// split, and can end in a failure.
type chunkBody struct {
	chunks    [][]byte
	failAfter error
	closed    bool
}

func (b *chunkBody) Read(p []byte) (int, error) {
	if len(b.chunks) == 0 {
		if b.failAfter != nil {
			return 0, b.failAfter
		}
		return 0, io.EOF
	}
	n := copy(p, b.chunks[0])
	if n < len(b.chunks[0]) {
		b.chunks[0] = b.chunks[0][n:]
	} else {
		b.chunks = b.chunks[1:]
	}
	return n, nil
}

func (b *chunkBody) Close() error {
	b.closed = true
	return nil
}

// answer is one scripted reply: a response, with its body kept so a test can ask what was read and closed, or a
// transport error.
type answer struct {
	response *imageResponse
	body     *chunkBody
	err      error
}

func responseAnswer(status int, header map[string]string, chunks ...[]byte) answer {
	body := &chunkBody{chunks: chunks}
	h := http.Header{}
	for key, value := range header {
		h.Set(key, value)
	}
	return answer{
		response: &imageResponse{status: status, reason: http.StatusText(status), header: h, body: body},
		body:     body,
	}
}

func errorAnswer(err error) answer { return answer{err: err} }

func statusAnswer(status int) answer { return responseAnswer(status, nil) }

// probeAnswer is what a server says to the one-byte Range probe.
func probeAnswer(status int, accept string) answer {
	header := map[string]string{}
	if accept != "" {
		header["Accept-Ranges"] = accept
	}
	return responseAnswer(status, header, []byte("Q"))
}

// bodySpec is a scripted full GET: the image split at one point into two reads, with the Content-Length it
// declares, which is the image's own length unless a test says otherwise.
type bodySpec struct {
	image     []byte
	declared  *string
	header    map[string]string
	split     int
	failAfter error
}

func withDeclared(text string) func(*bodySpec) { return func(s *bodySpec) { s.declared = &text } }
func withSplit(at int) func(*bodySpec)         { return func(s *bodySpec) { s.split = at } }
func withFailAfter(err error) func(*bodySpec)  { return func(s *bodySpec) { s.failAfter = err } }
func withHeader(key, value string) func(*bodySpec) {
	return func(s *bodySpec) {
		if s.header == nil {
			s.header = map[string]string{}
		}
		s.header[key] = value
	}
}

func bodyAnswer(image []byte, edit ...func(*bodySpec)) answer {
	spec := bodySpec{image: image, split: 100}
	for _, e := range edit {
		e(&spec)
	}
	declared := strconv.Itoa(len(spec.image))
	if spec.declared != nil {
		declared = *spec.declared
	}
	header := map[string]string{"Content-Length": declared}
	for key, value := range spec.header {
		header[key] = value
	}
	split := min(spec.split, len(spec.image))
	a := responseAnswer(http.StatusOK, header, spec.image[:split], spec.image[split:])
	a.body.failAfter = spec.failAfter
	return a
}

// fetchCall is one request the check made.
type fetchCall struct {
	url     string
	header  http.Header
	perRead time.Duration
}

// scriptedFetcher hands out scripted answers in order and remembers what it was asked.
type scriptedFetcher struct {
	mu      sync.Mutex
	answers []answer
	calls   []fetchCall
}

func newScripted(answers ...answer) *scriptedFetcher { return &scriptedFetcher{answers: answers} }

func (f *scriptedFetcher) open(_ context.Context, rawURL string, header http.Header, perRead time.Duration) (*imageResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fetchCall{url: rawURL, header: header.Clone(), perRead: perRead})
	if len(f.answers) == 0 {
		panic("the scripted fetcher has no answer left for " + rawURL)
	}
	next := f.answers[0]
	f.answers = f.answers[1:]
	if next.err != nil {
		return nil, next.err
	}
	return next.response, nil
}

func (f *scriptedFetcher) urls() []string {
	var urls []string
	for _, call := range f.calls {
		urls = append(urls, call.url)
	}
	return urls
}

// fakeClock moves by step each time it is read.
type fakeClock struct {
	at   time.Time
	step time.Duration
}

func (c *fakeClock) now() time.Time {
	current := c.at
	c.at = c.at.Add(c.step)
	return current
}

func steadyClock() func() time.Time { return func() time.Time { return time.Unix(1_000_000, 0) } }

func containsAll(t *testing.T, text string, fragments ...string) {
	t.Helper()
	for _, fragment := range fragments {
		if !strings.Contains(text, fragment) {
			t.Errorf("%q does not contain %q", text, fragment)
		}
	}
}
