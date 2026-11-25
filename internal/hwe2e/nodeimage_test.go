// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

// Contracts for the hardware E2E workflow's node image check.
//
// The transport is replaced by a scripted fetcher, so these say what the check requires of an answer and nothing
// about any server: the real fetch is exercised by running the command against a real URL, and that run is
// evidence of that URL only. The loopback tests at the end run the same code over a real socket; they say
// nothing about https or about any real server.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	nerrors "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const (
	imageURL  = "https://artifacts.example.test/jobs/7/artifacts/outputs/node-image.qcow2"
	imageSlot = "os-image-amd64"
	testRun   = "https://github.com/dsx-ai-factory/nvkr/actions/runs/123456789/attempts/1"
)

var (
	testImage  = newQCOW2().image(payloadOf("synthetic payload ", 64))
	testDigest = sumHex(testImage)
)

type imageOptions struct {
	url, sha256 string
	maxBytes    int64
	perRead     time.Duration
	whole       time.Duration
	now         func() time.Time
}

func withURL(u string) func(*imageOptions)     { return func(o *imageOptions) { o.url = u } }
func withSHA256(d string) func(*imageOptions)  { return func(o *imageOptions) { o.sha256 = d } }
func withMaxBytes(n int64) func(*imageOptions) { return func(o *imageOptions) { o.maxBytes = n } }
func withWhole(d time.Duration) func(*imageOptions) {
	return func(o *imageOptions) { o.whole = d }
}
func withClock(now func() time.Time) func(*imageOptions) {
	return func(o *imageOptions) { o.now = now }
}

func runImageCheck(fetch *scriptedFetcher, edit ...func(*imageOptions)) (map[string]any, error) {
	o := imageOptions{url: imageURL, sha256: testDigest, maxBytes: defaultMaxBytes, perRead: 60 * time.Second, whole: 30 * time.Minute, now: steadyClock()}
	for _, e := range edit {
		e(&o)
	}
	checker := newImageChecker(fetch, o.now, o.perRead, o.whole, o.maxBytes)
	return checker.check(context.Background(), imageSlot, o.url, o.sha256)
}

func goodScript() *scriptedFetcher {
	return newScripted(probeAnswer(200, "bytes"), bodyAnswer(testImage))
}

func mustCheck(t *testing.T, fetch *scriptedFetcher, edit ...func(*imageOptions)) map[string]any {
	t.Helper()
	record, err := runImageCheck(fetch, edit...)
	if err != nil {
		t.Fatalf("the check failed: %v", err)
	}
	return record
}

func mustFail(t *testing.T, fetch *scriptedFetcher, code nerrors.Code, fragment string, edit ...func(*imageOptions)) error {
	t.Helper()
	_, err := runImageCheck(fetch, edit...)
	assertCoded(t, err, code, fragment)
	return err
}

func canonical(t *testing.T, record map[string]any) string {
	t.Helper()
	data, err := marshalRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestImageCheckAcceptsTheNamedBytes(t *testing.T) {
	record := mustCheck(t, goodScript())
	want := map[string]any{
		"schema": "nvkr.dev/hardware-e2e-node-image-check/v1", "slot": imageSlot, "method": "http-get", "observedByThisRun": true,
		"url": imageURL, "sha256": testDigest, "bytes": len(testImage), "rangeProbe": "ignored", "rangeSupported": false,
		"acceptRanges": "bytes", "qcow2": map[string]any{"version": 3, "virtualSizeBytes": 12 << 30, "clusterSizeBytes": 65536},
	}
	if got, expected := canonical(t, record), canonical(t, want); got != expected {
		t.Errorf("record:\n%s\nwant:\n%s", got, expected)
	}
}

func TestRangeProbeIsRecordedNotRequired(t *testing.T) {
	t.Run("honored and ignored", func(t *testing.T) {
		honored := mustCheck(t, newScripted(probeAnswer(206, "bytes"), bodyAnswer(testImage)))
		if honored["rangeProbe"] != "honored" || honored["rangeSupported"] != true {
			t.Errorf("honored = %v", honored)
		}
		ignored := mustCheck(t, newScripted(probeAnswer(200, ""), bodyAnswer(testImage)))
		if ignored["rangeProbe"] != "ignored" || ignored["rangeSupported"] != false || ignored["acceptRanges"] != "" {
			t.Errorf("ignored = %v", ignored)
		}
	})

	t.Run("a server that rejects the probe is still checked by the full GET", func(t *testing.T) {
		// A server may refuse a Range request and serve the whole object; the full GET decides.
		for _, code := range []int{403, 405, 416, 500} {
			t.Run(strconv.Itoa(code), func(t *testing.T) {
				fetch := newScripted(statusAnswer(code), bodyAnswer(testImage))
				record := mustCheck(t, fetch)
				if record["rangeProbe"] != "unavailable" || record["rangeSupported"] != false || record["acceptRanges"] != "" {
					t.Errorf("record = %v", record)
				}
				if record["bytes"] != int64(len(testImage)) || record["sha256"] != testDigest {
					t.Errorf("the full GET did not decide: %v", record)
				}
				if !reflect.DeepEqual(fetch.urls(), []string{imageURL, imageURL}) {
					t.Errorf("urls = %v", fetch.urls())
				}
			})
		}
	})

	t.Run("an unusable probe answer is unavailable and the full GET still runs", func(t *testing.T) {
		answers := map[string]answer{
			"status 500":      probeAnswer(500, "bytes"),
			"status 404":      probeAnswer(404, ""),
			"a transport err": errorAnswer(errors.New("reset")),
			"a timeout":       errorAnswer(os.ErrDeadlineExceeded),
			"a 204":           probeAnswer(204, ""),
		}
		for name, probe := range answers {
			t.Run(name, func(t *testing.T) {
				record := mustCheck(t, newScripted(probe, bodyAnswer(testImage)))
				if record["rangeProbe"] != "unavailable" {
					t.Errorf("rangeProbe = %v", record["rangeProbe"])
				}
			})
		}
	})

	t.Run("a rejected probe never lets a bad full GET pass", func(t *testing.T) {
		other := newQCOW2().image(payloadOf("other ", 64))
		mustFail(t, newScripted(statusAnswer(416), bodyAnswer(other)), nerrors.CodeVerifyFailed, "not the")
		mustFail(t, newScripted(statusAnswer(416), statusAnswer(403)), nerrors.CodeVerifyFailed, "HTTP 403")
		encrypted := newQCOW2().with(func(s *qcow2Spec) { s.crypt = 2 }).image(bytes.Repeat([]byte("x"), 200))
		mustFail(t, newScripted(statusAnswer(416), bodyAnswer(encrypted)), nerrors.CodeUnsupported, "encrypted")
	})
}

func TestTheExactURLIsFetchedAnonymously(t *testing.T) {
	fetch := goodScript()
	mustCheck(t, fetch)
	if !reflect.DeepEqual(fetch.urls(), []string{imageURL, imageURL}) {
		t.Fatalf("urls = %v", fetch.urls())
	}
	probe, full := fetch.calls[0].header, fetch.calls[1].header
	if probe.Get("Range") != "bytes=0-0" {
		t.Errorf("probe Range = %q", probe.Get("Range"))
	}
	if full.Get("Range") != "" {
		t.Errorf("the full GET carries Range %q", full.Get("Range"))
	}
	for name, header := range map[string]http.Header{"probe": probe, "full": full} {
		if header.Get("Accept-Encoding") != "identity" {
			t.Errorf("%s Accept-Encoding = %q", name, header.Get("Accept-Encoding"))
		}
		if header.Get("User-Agent") != "nvkr-hardware-e2e-check-node-image" {
			t.Errorf("%s User-Agent = %q", name, header.Get("User-Agent"))
		}
		for _, credential := range []string{"Authorization", "Cookie", "PRIVATE-TOKEN", "JOB-TOKEN"} {
			if header.Get(credential) != "" {
				t.Errorf("%s carries %s", name, credential)
			}
		}
	}
	if probeTimeout := fetch.calls[0].perRead; probeTimeout != 60*time.Second {
		t.Errorf("the per-read limit reached the fetcher as %v", probeTimeout)
	}
}

func TestEveryResponseIsClosed(t *testing.T) {
	scripts := map[string][]answer{
		"a good check":          {probeAnswer(200, "bytes"), bodyAnswer(testImage)},
		"a wrong length":        {probeAnswer(200, "bytes"), bodyAnswer(testImage, withDeclared("1"))},
		"a truncated body":      {probeAnswer(200, "bytes"), bodyAnswer(testImage[:50], withDeclared(strconv.Itoa(len(testImage))))},
		"a rejected status":     {probeAnswer(200, "bytes"), statusAnswer(403)},
		"a failing probe":       {statusAnswer(500), bodyAnswer(testImage)},
		"a content coding":      {probeAnswer(200, "bytes"), bodyAnswer(testImage, withHeader("Content-Encoding", "gzip"))},
		"an unusable length":    {probeAnswer(200, "bytes"), bodyAnswer(testImage, withDeclared("many"))},
		"an unsupported header": {probeAnswer(200, "bytes"), bodyAnswer(newQCOW2().with(func(s *qcow2Spec) { s.crypt = 2 }).image(bytes.Repeat([]byte("x"), 200)))},
	}
	for name, answers := range scripts {
		t.Run(name, func(t *testing.T) {
			_, _ = runImageCheck(newScripted(answers...))
			for index, a := range answers {
				if a.body != nil && !a.body.closed {
					t.Errorf("answer %d was left open", index)
				}
			}
		})
	}
}

func TestUnsupportedURLsAreRefusedBeforeAnyRequest(t *testing.T) {
	urls := []string{
		"http://artifacts.example.test/node.qcow2",
		"https://user@artifacts.example.test/node.qcow2",
		"https://artifacts.example.test/node.qcow2?sig=synthetic",
		"https://artifacts.example.test/node.qcow2#part",
		"https://artifacts.example.test/node.raw",
		"https://artifacts.example.test/",
		"https://artifacts.example.test",
		"ftp://artifacts.example.test/node.qcow2",
		"https://artifacts.example.test/a b.qcow2",
		"https://artifacts.example.test/node.qcow2\n",
		"",
	}
	for _, u := range urls {
		t.Run(fmt.Sprintf("%q", u), func(t *testing.T) {
			fetch := goodScript()
			mustFail(t, fetch, nerrors.CodeUnsupported, "plain https URL", withURL(u))
			if len(fetch.calls) != 0 {
				t.Errorf("made %d requests", len(fetch.calls))
			}
		})
	}
}

func TestHTTPFailures(t *testing.T) {
	t.Run("an HTTP error is a failure naming its status", func(t *testing.T) {
		for _, code := range []int{401, 403, 404, 500} {
			t.Run(strconv.Itoa(code), func(t *testing.T) {
				mustFail(t, newScripted(statusAnswer(code), statusAnswer(code)), nerrors.CodeVerifyFailed, fmt.Sprintf("HTTP %d", code))
				mustFail(t, newScripted(probeAnswer(200, ""), statusAnswer(code)), nerrors.CodeVerifyFailed, fmt.Sprintf("HTTP %d", code))
			})
		}
	})

	t.Run("a redirect is reported and not followed", func(t *testing.T) {
		fetch := newScripted(statusAnswer(302), statusAnswer(302))
		mustFail(t, fetch, nerrors.CodeVerifyFailed, "redirect")
		if !reflect.DeepEqual(fetch.urls(), []string{imageURL, imageURL}) {
			t.Errorf("urls = %v", fetch.urls())
		}
	})

	t.Run("a transport failure is a failure", func(t *testing.T) {
		mustFail(t, newScripted(errorAnswer(errors.New("no route")), errorAnswer(errors.New("no route"))), nerrors.CodeVerifyFailed, "could not be fetched")
		mustFail(t, newScripted(probeAnswer(200, ""), errorAnswer(os.ErrDeadlineExceeded)), nerrors.CodeVerifyFailed, "could not be fetched")
		dropped := bodyAnswer(testImage[:100], withDeclared(strconv.Itoa(len(testImage))), withFailAfter(errors.New("reset by peer")))
		mustFail(t, newScripted(probeAnswer(200, ""), dropped), nerrors.CodeVerifyFailed, "failed after 100 of")
	})

	t.Run("a non-success answer is a failure", func(t *testing.T) {
		mustFail(t, newScripted(probeAnswer(200, ""), responseAnswer(500, map[string]string{"Content-Length": "5"}, []byte("12345"))), nerrors.CodeVerifyFailed, "HTTP 500")
		mustFail(t, newScripted(probeAnswer(200, ""), responseAnswer(206, map[string]string{"Content-Length": "5"}, []byte("12345"))), nerrors.CodeVerifyFailed, "not 200")
	})

	t.Run("an interrupted run is a timeout, not the server's fault", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		checker := newImageChecker(newScripted(errorAnswer(context.Canceled), errorAnswer(context.Canceled)), steadyClock(), time.Second, time.Minute, defaultMaxBytes)
		_, err := checker.check(ctx, imageSlot, imageURL, testDigest)
		assertCoded(t, err, nerrors.CodeTimeout, "was interrupted")
	})
}

func TestTheBodyMustDeclareAndMatchItsLength(t *testing.T) {
	cases := []struct {
		name     string
		body     answer
		fragment string
	}{
		{"no length", bodyAnswer(testImage, withDeclared("")), "Content-Length"},
		{"not a number", bodyAnswer(testImage, withDeclared("many")), "Content-Length"},
		{"a signed number", bodyAnswer(testImage, withDeclared("+5")), "Content-Length"},
		{"zero", bodyAnswer(testImage, withDeclared("0")), "Content-Length"},
		{"a negative number too large for 64 bits", bodyAnswer(testImage, withDeclared("-99999999999999999999")), "no usable Content-Length"},
		{"a count no 64-bit length holds is over the limit", bodyAnswer(testImage, withDeclared("99999999999999999999999")), "declares 99999999999999999999999 bytes, over the"},
		{"short", bodyAnswer(testImage, withDeclared(strconv.Itoa(len(testImage)+1))), fmt.Sprintf("sent %d of its declared", len(testImage))},
		{"long", bodyAnswer(testImage, withDeclared(strconv.Itoa(len(testImage)-1))), "more than its declared"},
		{"cut off by the connection", bodyAnswer(testImage[:100], withDeclared(strconv.Itoa(len(testImage))), withFailAfter(errors.New("unexpected EOF"))), "failed after 100 of"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mustFail(t, newScripted(probeAnswer(200, ""), tc.body), nerrors.CodeVerifyFailed, tc.fragment)
		})
	}

	t.Run("an unexpected EOF reads as the short body it is", func(t *testing.T) {
		cut := bodyAnswer(testImage[:100], withDeclared(strconv.Itoa(len(testImage))), withFailAfter(fmt.Errorf("transport: %w", io.ErrUnexpectedEOF)))
		mustFail(t, newScripted(probeAnswer(200, ""), cut), nerrors.CodeVerifyFailed, "sent 100 of its declared")
	})
}

func TestBodyLimits(t *testing.T) {
	t.Run("a body over the size limit is refused before it is read", func(t *testing.T) {
		answerBody := bodyAnswer(testImage)
		mustFail(t, newScripted(probeAnswer(200, ""), answerBody), nerrors.CodeVerifyFailed, "byte limit", withMaxBytes(int64(len(testImage)-1)))
		if len(answerBody.body.chunks) != 2 {
			t.Errorf("%d chunks left, want both unread", len(answerBody.body.chunks))
		}
	})

	t.Run("a content encoding means the wire bytes are not the image", func(t *testing.T) {
		mustFail(t, newScripted(probeAnswer(200, ""), bodyAnswer(testImage, withHeader("Content-Encoding", "gzip"))), nerrors.CodeVerifyFailed, "Content-Encoding gzip")
		identity := mustCheck(t, newScripted(probeAnswer(200, ""), bodyAnswer(testImage, withHeader("Content-Encoding", "Identity"))))
		if identity["bytes"] != int64(len(testImage)) {
			t.Errorf("identity coding was refused: %v", identity)
		}
	})

	t.Run("a digest that differs names both", func(t *testing.T) {
		other := sumHex([]byte("another image"))
		err := mustFail(t, goodScript(), nerrors.CodeVerifyFailed, "not the", withSHA256(other))
		containsAll(t, err.Error(), testDigest, other)
	})

	t.Run("a slow body is cut off", func(t *testing.T) {
		clock := &fakeClock{at: time.Unix(0, 0), step: 1000 * time.Second}
		mustFail(t, goodScript(), nerrors.CodeVerifyFailed, "still streaming", withWhole(10*time.Second), withClock(clock.now))
	})
}

func TestImagesTheImagerCannotUse(t *testing.T) {
	filler := bytes.Repeat([]byte("x"), 200)
	spec := func(edit func(*qcow2Spec)) []byte { return newQCOW2().with(edit).image(filler) }
	cases := []struct {
		name     string
		image    []byte
		fragment string
	}{
		{"wrong magic", spec(func(s *qcow2Spec) { s.magic = []byte{'Q', 'F', 'I', 0} }), "magic"},
		{"version 1", spec(func(s *qcow2Spec) { s.version = 1 }), "version 1"},
		{"version 4", spec(func(s *qcow2Spec) { s.version = 4 }), "version 4"},
		{"backing file", spec(func(s *qcow2Spec) { s.backing = 4096 }), "backing file"},
		{"qcow2-layer encryption", spec(func(s *qcow2Spec) { s.crypt = 2 }), "encrypted"},
		{"absurd cluster size", spec(func(s *qcow2Spec) { s.bits = 3 }), "cluster size"},
		{"huge cluster size", spec(func(s *qcow2Spec) { s.bits = 22 }), "cluster size"},
		{"no virtual size", spec(func(s *qcow2Spec) { s.size = 0 }), "no virtual size"},
		{"version 3 with an external data file", spec(func(s *qcow2Spec) { s.incompatible = 1 << 2 }), "external data file"},
		{"version 3 with an external data file and other bits", spec(func(s *qcow2Spec) { s.incompatible = 1<<2 | 1 }), "external data file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mustFail(t, newScripted(probeAnswer(200, ""), bodyAnswer(tc.image)), nerrors.CodeUnsupported, tc.fragment, withSHA256(sumHex(tc.image)))
		})
	}

	t.Run("an image too small for a header", func(t *testing.T) {
		image := append([]byte{'Q', 'F', 'I', 0xfb}, make([]byte, 10)...)
		mustFail(t, newScripted(probeAnswer(200, ""), bodyAnswer(image, withSplit(4))), nerrors.CodeUnsupported, "too small", withSHA256(sumHex(image)))
	})

	t.Run("a header split across chunks is still read", func(t *testing.T) {
		// 80 is past the version 2 header and short of the version 3 one: the check waits for the rest.
		for _, split := range []int{10, qcow2HeaderBytes, 80, qcow2V3HeaderBytes - 1} {
			t.Run(strconv.Itoa(split), func(t *testing.T) {
				record := mustCheck(t, newScripted(probeAnswer(200, ""), bodyAnswer(testImage, withSplit(split))))
				if record["qcow2"].(map[string]any)["version"] != 3 {
					t.Errorf("qcow2 = %v", record["qcow2"])
				}
			})
		}
	})

	t.Run("a version 3 header with its feature fields is what is read", func(t *testing.T) {
		// The external-data-file flag sits after the 72-byte base header, so a base-header parse would pass it.
		image := spec(func(s *qcow2Spec) { s.incompatible = 1 << 2 })
		mustFail(t, newScripted(probeAnswer(200, ""), bodyAnswer(image, withSplit(80))), nerrors.CodeUnsupported, "external data file", withSHA256(sumHex(image)))
	})

	t.Run("other incompatible feature bits do not decide this check", func(t *testing.T) {
		image := spec(func(s *qcow2Spec) { s.incompatible = 1 })
		record := mustCheck(t, newScripted(probeAnswer(200, ""), bodyAnswer(image)), withSHA256(sumHex(image)))
		if record["qcow2"].(map[string]any)["version"] != 3 {
			t.Errorf("qcow2 = %v", record["qcow2"])
		}
	})

	t.Run("a version 2 image is read from its 72-byte header", func(t *testing.T) {
		header := newQCOW2().with(func(s *qcow2Spec) { s.version = 2 }).header()
		if len(header) != qcow2HeaderBytes {
			t.Fatalf("a version 2 header is %d bytes, want %d", len(header), qcow2HeaderBytes)
		}
		image := append(header, filler...)
		record := mustCheck(t, newScripted(probeAnswer(200, ""), bodyAnswer(image)), withSHA256(sumHex(image)))
		if record["qcow2"].(map[string]any)["version"] != 2 {
			t.Errorf("qcow2 = %v", record["qcow2"])
		}
	})

	t.Run("a version 3 header cut short is too small", func(t *testing.T) {
		image := newQCOW2().header()[:90]
		mustFail(t, newScripted(probeAnswer(200, ""), bodyAnswer(image, withSplit(90))), nerrors.CodeUnsupported, "version 3 qcow2 header", withSHA256(sumHex(image)))
	})

	t.Run("an unsupported image is refused before the rest is streamed", func(t *testing.T) {
		image := spec(func(s *qcow2Spec) { s.crypt = 2 })
		response := bodyAnswer(image, withSplit(qcow2V3HeaderBytes))
		mustFail(t, newScripted(probeAnswer(200, ""), response), nerrors.CodeUnsupported, "encrypted", withSHA256(sumHex(image)))
		if len(response.body.chunks) != 1 {
			t.Errorf("%d chunks left, want the second unread", len(response.body.chunks))
		}
	})
}

func TestEveryReadIsBoundedByTheTimeLeft(t *testing.T) {
	var limits []time.Duration
	response := bodyAnswer(testImage)
	response.response.limit = func(d time.Duration) { limits = append(limits, d) }
	// The clock moves 3s per reading; the whole-body deadline is 10s and each read may wait up to 60s.
	clock := &fakeClock{at: time.Unix(0, 0), step: 3 * time.Second}
	mustCheck(t, newScripted(probeAnswer(200, ""), response), withWhole(10*time.Second), withClock(clock.now))
	want := []time.Duration{7 * time.Second, 4 * time.Second, 1 * time.Second}
	if !reflect.DeepEqual(limits, want) {
		t.Errorf("read limits = %v, want what is left of the deadline each time: %v", limits, want)
	}

	t.Run("the inactivity limit applies while there is time", func(t *testing.T) {
		limits = nil
		response := bodyAnswer(testImage)
		response.response.limit = func(d time.Duration) { limits = append(limits, d) }
		clock := &fakeClock{at: time.Unix(0, 0), step: 2 * time.Second}
		mustCheck(t, newScripted(probeAnswer(200, ""), response), withWhole(100*time.Second), withClock(clock.now))
		if len(limits) == 0 || limits[0] != 60*time.Second || !sort.SliceIsSorted(limits, func(i, j int) bool { return limits[i] > limits[j] }) {
			t.Errorf("read limits = %v, want 60s first and never rising", limits)
		}
	})
}

// imageHandler behaves like the object stores this lane has to cope with, over a loopback socket.
type imageHandler struct {
	mu       sync.Mutex
	requests []string
	headers  []http.Header
}

func (h *imageHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.requests = append(h.requests, r.Method+" "+r.URL.Path)
	h.headers = append(h.headers, r.Header.Clone())
	h.mu.Unlock()
	switch {
	case r.URL.Path == "/redirect.qcow2":
		w.Header().Set("Location", "/image.qcow2")
		w.WriteHeader(http.StatusFound)
	case r.URL.Path == "/head-only.qcow2" && r.Method == http.MethodGet:
		w.WriteHeader(http.StatusForbidden) // HEAD answers 200 for the same path, as GitLab artifact downloads do
	case r.URL.Path == "/trickle.qcow2" && r.Method == http.MethodGet:
		// One byte at a time, each inside any socket timeout, for far longer than the check will wait.
		w.Header().Set("Content-Length", strconv.Itoa(1<<20))
		w.WriteHeader(http.StatusOK)
		for range 100 {
			if _, err := w.Write([]byte{0}); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
	case r.URL.Path == "/stall.qcow2" && r.Method == http.MethodGet:
		// Some of the body, then silence: only a read deadline ends this before the whole-body deadline.
		w.Header().Set("Content-Length", strconv.Itoa(len(testImage)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(testImage[:100])
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	case r.URL.Path == "/image.qcow2" || r.URL.Path == "/head-only.qcow2":
		w.Header().Set("Content-Length", strconv.Itoa(len(testImage)))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(testImage)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (h *imageHandler) paths() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.requests...)
}

func startLoopback(t *testing.T, tls bool) (*httptest.Server, *imageHandler) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen on loopback here: %v", err)
	}
	handler := &imageHandler{}
	server := &httptest.Server{Listener: listener, Config: &http.Server{Handler: handler}}
	if tls {
		server.StartTLS()
	} else {
		server.Start()
	}
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})
	return server, handler
}

func loopbackChecker(perRead, whole time.Duration) *imageChecker {
	return newImageChecker(httpFetcher{}, time.Now, perRead, whole, defaultMaxBytes)
}

func TestRealTransport(t *testing.T) {
	t.Run("streams the body and checks it", func(t *testing.T) {
		server, handler := startLoopback(t, false)
		size, info, err := loopbackChecker(5*time.Second, time.Minute).stream(context.Background(), server.URL+"/image.qcow2", testDigest)
		if err != nil {
			t.Fatal(err)
		}
		if size != int64(len(testImage)) || info.version != 3 {
			t.Errorf("size = %d, version = %d", size, info.version)
		}
		if !reflect.DeepEqual(handler.paths(), []string{"GET /image.qcow2"}) {
			t.Errorf("requests = %v", handler.paths())
		}
		header := handler.headers[0]
		if header.Get("Authorization") != "" || header.Get("Accept-Encoding") != "identity" || header.Get("User-Agent") != "nvkr-hardware-e2e-check-node-image" {
			t.Errorf("request headers = %v", header)
		}
	})

	t.Run("a redirect is an error and its target is never requested", func(t *testing.T) {
		server, handler := startLoopback(t, false)
		_, _, err := loopbackChecker(5*time.Second, time.Minute).stream(context.Background(), server.URL+"/redirect.qcow2", testDigest)
		assertCoded(t, err, nerrors.CodeVerifyFailed, "HTTP 302")
		if !reflect.DeepEqual(handler.paths(), []string{"GET /redirect.qcow2"}) {
			t.Errorf("requests = %v", handler.paths())
		}
	})

	t.Run("a URL that answers HEAD but refuses GET fails", func(t *testing.T) {
		server, _ := startLoopback(t, false)
		// No proxy: a proxy set in the environment would answer for a loopback address.
		client := &http.Client{Transport: &http.Transport{Proxy: nil}}
		head, err := client.Head(server.URL + "/head-only.qcow2")
		if err != nil {
			t.Fatal(err)
		}
		_ = head.Body.Close()
		if head.StatusCode != http.StatusOK {
			t.Fatalf("HEAD = %d, want 200", head.StatusCode)
		}
		_, _, err = loopbackChecker(5*time.Second, time.Minute).stream(context.Background(), server.URL+"/head-only.qcow2", testDigest)
		assertCoded(t, err, nerrors.CodeVerifyFailed, "HTTP 403")
	})

	t.Run("a missing object fails at the full GET, not the probe", func(t *testing.T) {
		server, _ := startLoopback(t, false)
		checker := loopbackChecker(5*time.Second, time.Minute)
		if probe, accept := checker.probeRange(context.Background(), server.URL+"/gone.qcow2"); probe != "unavailable" || accept != "" {
			t.Errorf("probe = %q, %q", probe, accept)
		}
		_, _, err := checker.stream(context.Background(), server.URL+"/gone.qcow2", testDigest)
		assertCoded(t, err, nerrors.CodeVerifyFailed, "HTTP 404")
	})

	t.Run("the probe records that this server ignores Range", func(t *testing.T) {
		server, _ := startLoopback(t, false)
		if probe, accept := loopbackChecker(5*time.Second, time.Minute).probeRange(context.Background(), server.URL+"/image.qcow2"); probe != "ignored" || accept != "" {
			t.Errorf("probe = %q, %q", probe, accept)
		}
	})

	t.Run("a server that trickles bytes is cut off at the deadline, not the socket timeout", func(t *testing.T) {
		// Each byte arrives inside the 5 second socket timeout, so only a deadline that interrupts the read ends this.
		server, _ := startLoopback(t, false)
		started := time.Now()
		_, _, err := loopbackChecker(5*time.Second, time.Second).stream(context.Background(), server.URL+"/trickle.qcow2", testDigest)
		assertCoded(t, err, nerrors.CodeVerifyFailed, "still streaming after 1s")
		if elapsed := time.Since(started); elapsed > 4*time.Second {
			t.Errorf("took %v to give up on a 1s deadline", elapsed)
		}
	})

	t.Run("a server that goes silent mid-body is cut off by the read timeout", func(t *testing.T) {
		// The whole-body deadline is a minute away, so only the per-read deadline ends this. The context bounds the
		// test: without that deadline the read would wait for it, and the failure would be a hang.
		server, _ := startLoopback(t, false)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		started := time.Now()
		_, _, err := loopbackChecker(time.Second, time.Minute).stream(ctx, server.URL+"/stall.qcow2", testDigest)
		assertCoded(t, err, nerrors.CodeVerifyFailed, "failed after 100 of")
		if elapsed := time.Since(started); elapsed > 5*time.Second {
			t.Errorf("took %v to give up on a 1s read timeout", elapsed)
		}
	})

	t.Run("a certificate the runner does not trust is a failure, not a warning", func(t *testing.T) {
		server, _ := startLoopback(t, true)
		_, _, err := loopbackChecker(5*time.Second, time.Minute).stream(context.Background(), server.URL+"/image.qcow2", testDigest)
		assertCoded(t, err, nerrors.CodeVerifyFailed, "could not be fetched")
		if !strings.Contains(err.Error(), "certificate") {
			t.Errorf("error = %v, want the certificate named", err)
		}
	})

	t.Run("the canceled run stops the fetch", func(t *testing.T) {
		server, _ := startLoopback(t, false)
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		started := time.Now()
		_, _, err := loopbackChecker(5*time.Second, time.Minute).stream(ctx, server.URL+"/trickle.qcow2", testDigest)
		assertCoded(t, err, nerrors.CodeTimeout, "was interrupted")
		if elapsed := time.Since(started); elapsed > 3*time.Second {
			t.Errorf("took %v to stop", elapsed)
		}
	})
}

func TestTheTransportFetchesLikeTheImager(t *testing.T) {
	transport := newTransport(time.Second, &connHolder{})
	if transport.Proxy != nil {
		t.Error("the transport consults a proxy; an ambient proxy would change what is being checked")
	}
	if !transport.DisableCompression || !transport.DisableKeepAlives {
		t.Error("the transport must not decode a content coding or keep a connection")
	}
}

func TestWriteNewFile(t *testing.T) {
	// The pre-check in the commands is a courtesy; the exclusive create is what refuses a race.
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing.json")
	if err := os.WriteFile(existing, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.json")
	if err := os.Symlink(filepath.Join(dir, "elsewhere"), link); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	for name, path := range map[string]string{"an existing file": existing, "a symlink": link, "a missing directory": filepath.Join(dir, "missing", "out.json")} {
		t.Run(name, func(t *testing.T) {
			err := writeNewFile(path, []byte("{}\n"), 0o600)
			assertCoded(t, err, nerrors.CodeImmutableViolation, "")
			if nerrors.ExitCode(err) != 47 {
				t.Errorf("exit = %d, want 47", nerrors.ExitCode(err))
			}
		})
	}
	if got, _ := os.ReadFile(existing); string(got) != "keep\n" {
		t.Errorf("the existing file was changed: %q", got)
	}
	if pathExists(filepath.Join(dir, "elsewhere")) {
		t.Error("a write went through the symlink")
	}
}

// --- the command ---

func execNodeImage(t *testing.T, fetch fetcher, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := checkNodeImage(context.Background(), args, &stdout, &stderr, fetch, steadyClock())
	return report(&stderr, err), stdout.String(), stderr.String()
}

func TestNodeImageCommand(t *testing.T) {
	t.Run("writes one private record and says so", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "check.json")
		code, stdout, stderr := execNodeImage(t, goodScript(), "--slot", imageSlot, "--url", imageURL, "--sha256", testDigest, "--out", out)
		if code != 0 {
			t.Fatalf("exit = %d: %s", code, stderr)
		}
		containsAll(t, stdout, fmt.Sprintf("node image ok: %s, %d bytes", imageSlot, len(testImage)))
		info, err := os.Stat(out)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("record mode = %v, err = %v", info, err)
		}
		data, _ := os.ReadFile(out)
		var record map[string]any
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatal(err)
		}
		keys := make([]string, 0, len(record))
		for key := range record {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		want := []string{"acceptRanges", "bytes", "method", "observedByThisRun", "qcow2", "rangeProbe", "rangeSupported", "schema", "sha256", "slot", "url"}
		if !reflect.DeepEqual(keys, want) {
			t.Errorf("record keys = %v, want %v", keys, want)
		}
	})

	t.Run("a failure writes nothing and carries its code", func(t *testing.T) {
		encrypted := newQCOW2().with(func(s *qcow2Spec) { s.crypt = 2 }).image(bytes.Repeat([]byte("x"), 200))
		cases := []struct {
			name  string
			fetch fetcher
			url   string
			exit  int
			code  nerrors.Code
		}{
			{"the server forbids it", newScripted(statusAnswer(403), statusAnswer(403)), imageURL, 22, nerrors.CodeVerifyFailed},
			{"an http URL", goodScript(), "http://artifacts.example.test/node.qcow2", 13, nerrors.CodeUnsupported},
			{"an encrypted image", newScripted(probeAnswer(200, ""), bodyAnswer(encrypted)), imageURL, 13, nerrors.CodeUnsupported},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				out := filepath.Join(t.TempDir(), "check.json")
				code, _, stderr := execNodeImage(t, tc.fetch, "--slot", imageSlot, "--url", tc.url, "--sha256", testDigest, "--out", out)
				if code != tc.exit || !strings.Contains(stderr, "["+string(tc.code)+"]") {
					t.Errorf("exit = %d, stderr = %s", code, stderr)
				}
				if pathExists(out) {
					t.Error("a failed check left a record")
				}
			})
		}
	})

	t.Run("never replaces an existing output and asks nothing first", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "check.json")
		if err := os.WriteFile(out, []byte("keep\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		fetch := goodScript()
		code, _, stderr := execNodeImage(t, fetch, "--slot", imageSlot, "--url", imageURL, "--sha256", testDigest, "--out", out)
		if got, _ := os.ReadFile(out); code != 47 || string(got) != "keep\n" || len(fetch.calls) != 0 {
			t.Errorf("exit = %d, content = %q, calls = %d", code, got, len(fetch.calls))
		}
		containsAll(t, stderr, "[NVKR-E-IMMUTABLE-VIOLATION]")
	})

	t.Run("does not write through a symlink", func(t *testing.T) {
		dir := t.TempDir()
		out, target := filepath.Join(dir, "check.json"), filepath.Join(dir, "elsewhere")
		if err := os.Symlink(target, out); err != nil {
			t.Skipf("no symlinks here: %v", err)
		}
		fetch := goodScript()
		code, _, _ := execNodeImage(t, fetch, "--slot", imageSlot, "--url", imageURL, "--sha256", testDigest, "--out", out)
		if code != 47 || pathExists(target) || len(fetch.calls) != 0 {
			t.Errorf("exit = %d, target exists = %v, calls = %d", code, pathExists(target), len(fetch.calls))
		}
	})

	t.Run("usage errors exit 2 and name the usage code", func(t *testing.T) {
		upper := strings.ToUpper(testDigest)
		cases := [][]string{
			{"--slot", "os-image-ppc64le", "--url", imageURL, "--sha256", testDigest, "--out", "x"},
			{"--slot", imageSlot, "--url", imageURL, "--sha256", upper, "--out", "x"},
			{"--slot", imageSlot, "--url", imageURL, "--sha256", testDigest[:63], "--out", "x"},
			{"--slot", imageSlot, "--url", imageURL, "--sha256", testDigest},
			{"--slot", imageSlot, "--url", imageURL, "--sha256", testDigest, "--out", "x", "--timeout", "0"},
			{"--slot", imageSlot, "--url", imageURL, "--sha256", testDigest, "--out", "x", "--max-seconds", "-1"},
			{"--slot", imageSlot, "--url", imageURL, "--sha256", testDigest, "--out", "x", "--max-bytes", "many"},
			{"--slot", imageSlot, "--out", "x"},
			{"--slot", imageSlot, "--url", imageURL, "--out", "x"},
			{"--slot", imageSlot, "--sha256", testDigest, "--out", "x"},
			{"--slot", imageSlot, "--substitutions", "s.json", "--url", imageURL, "--out", "x"},
			{"--slot", imageSlot, "--substitutions", "s.json", "--sha256", testDigest, "--out", "x"},
			{"--slot", imageSlot, "--url", imageURL, "--sha256", testDigest, "--out", "x", "extra"},
			{},
		}
		for _, args := range cases {
			t.Run(strings.Join(args, " "), func(t *testing.T) {
				code, _, stderr := execNodeImage(t, goodScript(), args...)
				if code != 2 || !strings.Contains(stderr, "[NVKR-E-USAGE]") {
					t.Errorf("exit = %d, stderr = %q", code, stderr)
				}
			})
		}
	})
}

// --- verification records ---

// missing marks a field a test leaves out.
type missing struct{}

func readback(changes map[string]any) map[string]any {
	value := map[string]any{"method": "s3-readback", "run": testRun, "bytes": len(testImage), "qcow2Version": 3, "virtualSizeBytes": 12 << 30}
	for key, change := range changes {
		if _, drop := change.(missing); drop {
			delete(value, key)
			continue
		}
		value[key] = change
	}
	return value
}

// decoded round-trips a value through JSON text, the way the substitution record reaches the command, so numbers
// arrive as the integers and floats the text spelled.
func decoded(t *testing.T, value any) any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	out, err := decodeJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestParseVerification(t *testing.T) {
	t.Run("accepts the two methods", func(t *testing.T) {
		cases := map[string]any{
			"http-get":      map[string]any{"method": "http-get"},
			"read-back":     readback(nil),
			"another run":   readback(map[string]any{"run": "https://github.com/o/r/actions/runs/9"}),
			"qcow2 version": readback(map[string]any{"qcow2Version": 2}),
		}
		for name, value := range cases {
			t.Run(name, func(t *testing.T) {
				if _, err := parseVerification(decoded(t, value)); err != nil {
					t.Errorf("refused: %v", err)
				}
			})
		}
	})

	t.Run("returns what the read-back says", func(t *testing.T) {
		v, err := parseVerification(decoded(t, readback(nil)))
		want := verification{method: "s3-readback", run: testRun, bytes: int64(len(testImage)), virtualBytes: 12 << 30, qcow2Version: 3}
		if err != nil || v != want {
			t.Errorf("verification = %+v, %v; want %+v", v, err, want)
		}
	})

	t.Run("refuses what the lane does not understand", func(t *testing.T) {
		raw := func(text string) json.RawMessage { return json.RawMessage(text) }
		runWith := func(run string) map[string]any { return readback(map[string]any{"run": run}) }
		cases := []struct {
			name     string
			value    any
			fragment string
		}{
			{"not an object", nil, "must be an object"},
			{"a string", "http-get", "must be an object"},
			{"a list", []any{map[string]any{"method": "http-get"}}, "must be an object"},
			{"no method", map[string]any{}, "verification method must be http-get or s3-readback, got null"},
			{"an unknown method", map[string]any{"method": "head-object"}, `verification method must be http-get or s3-readback, got "head-object"`},
			{"a method in another case", map[string]any{"method": "HTTP-GET"}, `verification method must be http-get or s3-readback, got "HTTP-GET"`},
			{"a method that is not text", map[string]any{"method": 5}, "verification method must be http-get or s3-readback, got 5"},
			{"http-get with a field", map[string]any{"method": "http-get", "run": testRun}, "takes no other field"},
			{"read-back without run", readback(map[string]any{"run": missing{}}), "needs exactly"},
			{"read-back without bytes", readback(map[string]any{"bytes": missing{}}), "needs exactly"},
			{"read-back without version", readback(map[string]any{"qcow2Version": missing{}}), "needs exactly"},
			{"read-back without virtual size", readback(map[string]any{"virtualSizeBytes": missing{}}), "needs exactly"},
			{"read-back with a field", readback(map[string]any{"extra": 1}), "needs exactly"},
			{"a run over http", runWith(strings.Replace(testRun, "https", "http", 1)), "GitHub Actions run"},
			{"a run on another host", runWith(strings.Replace(testRun, "github.com", "gitlab.example.test", 1)), "GitHub Actions run"},
			{"a run that is a repository", runWith("https://github.com/dsx-ai-factory/nvkr"), "GitHub Actions run"},
			{"run zero", runWith("https://github.com/o/r/actions/runs/0"), "GitHub Actions run"},
			{"attempt zero", runWith("https://github.com/o/r/actions/runs/5/attempts/0"), "GitHub Actions run"},
			{"a run with a query", runWith(testRun + "?x=1"), "GitHub Actions run"},
			{"a run that is a number", readback(map[string]any{"run": 123}), "GitHub Actions run"},
			{"a run with text before it", runWith("see " + testRun), "GitHub Actions run"},
			{"a run that ends in a newline", runWith(testRun + "\n"), "GitHub Actions run"},
			{"zero bytes", readback(map[string]any{"bytes": 0}), "positive integers"},
			{"negative bytes", readback(map[string]any{"bytes": -1}), "positive integers"},
			{"true bytes", readback(map[string]any{"bytes": true}), "positive integers"},
			{"fractional bytes", readback(map[string]any{"bytes": raw("1.5")}), "positive integers"},
			{"a float spelled as a whole number", readback(map[string]any{"bytes": raw("2.0")}), "positive integers"},
			{"an exponent", readback(map[string]any{"bytes": raw("1e3")}), "positive integers"},
			{"text bytes", readback(map[string]any{"bytes": "1"}), "positive integers"},
			{"a size no 64-bit count holds", readback(map[string]any{"bytes": raw("9223372036854775808")}), "positive integers"},
			{"a virtual size no 64-bit count holds", readback(map[string]any{"virtualSizeBytes": raw("99999999999999999999")}), "positive integers"},
			{"zero virtual size", readback(map[string]any{"virtualSizeBytes": 0}), "positive integers"},
			{"true virtual size", readback(map[string]any{"virtualSizeBytes": true}), "positive integers"},
			{"version one", readback(map[string]any{"qcow2Version": 1}), "qcow2Version"},
			{"version four", readback(map[string]any{"qcow2Version": 4}), "qcow2Version"},
			{"true version", readback(map[string]any{"qcow2Version": true}), "qcow2Version"},
			{"text version", readback(map[string]any{"qcow2Version": "3"}), "qcow2Version"},
			{"float version", readback(map[string]any{"qcow2Version": raw("3.0")}), "qcow2Version"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				_, err := parseVerification(decoded(t, tc.value))
				assertCoded(t, err, nerrors.CodeUnsupported, tc.fragment)
				if nerrors.ExitCode(err) != 13 {
					t.Errorf("exit = %d, want 13", nerrors.ExitCode(err))
				}
			})
		}
	})
}

func TestRecorded(t *testing.T) {
	t.Run("describes what the publication reported and says this run did not see it", func(t *testing.T) {
		v, err := parseVerification(decoded(t, readback(nil)))
		if err != nil {
			t.Fatal(err)
		}
		record, err := recorded(imageSlot, imageURL, testDigest, v)
		if err != nil {
			t.Fatal(err)
		}
		want := map[string]any{
			"schema": "nvkr.dev/hardware-e2e-node-image-check/v1", "slot": imageSlot, "method": "s3-readback", "observedByThisRun": false,
			"run": testRun, "url": imageURL, "sha256": testDigest, "bytes": len(testImage),
			"qcow2": map[string]any{"version": 3, "virtualSizeBytes": 12 << 30},
		}
		if got, expected := canonical(t, record), canonical(t, want); got != expected {
			t.Errorf("record:\n%s\nwant:\n%s", got, expected)
		}
	})

	t.Run("refuses a location the lane does not support", func(t *testing.T) {
		for _, u := range []string{
			"http://artifacts.example.test/a.qcow2", "https://artifacts.example.test/a.raw", "https://user@artifacts.example.test/a.qcow2",
			"https://artifacts.example.test/a.qcow2?token=1", "oci://artifacts.example.test/a@sha256:" + strings.Repeat("a", 64),
			"https://artifacts.example.test/a.qcow2\n",
		} {
			t.Run(u, func(t *testing.T) {
				_, err := recorded(imageSlot, u, testDigest, verification{method: "s3-readback"})
				assertCoded(t, err, nerrors.CodeUnsupported, "plain https URL")
			})
		}
	})
}

func replacement(verification any, edit map[string]any) map[string]any {
	entry := map[string]any{"slot": imageSlot, "from": "https://nvcr.example.test/old.qcow2", "to": imageURL, "sha256": testDigest,
		"source": map[string]any{"kind": "nvkr-release", "reference": "oci://ghcr.io/example/release@sha256:" + strings.Repeat("b", 64), "version": "v0.24.0-beta.12"}}
	if _, none := verification.(missing); !none {
		entry["verification"] = verification
	}
	for key, value := range edit {
		if _, drop := value.(missing); drop {
			delete(entry, key)
			continue
		}
		entry[key] = value
	}
	return entry
}

func substitutionsRecord(top map[string]any, replaced ...any) map[string]any {
	record := map[string]any{"schema": "nvkr.dev/hardware-e2e-substitutions/v1", "artifactSource": "nvkr-outputs", "replaced": replaced}
	for key, value := range top {
		if _, drop := value.(missing); drop {
			delete(record, key)
			continue
		}
		record[key] = value
	}
	return record
}

func writeJSONFile(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestNamedIn(t *testing.T) {
	t.Run("returns what the record names for the slot asked", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "substitutions.json")
		arm := "https://artifacts.example.test/arm.qcow2"
		writeJSONFile(t, path, substitutionsRecord(nil,
			replacement(map[string]any{"method": "http-get"}, map[string]any{"slot": "os-image-arm64", "to": arm, "sha256": strings.Repeat("c", 64)}),
			replacement(readback(nil), nil)))
		u, digest, v, err := namedIn(path, imageSlot)
		if err != nil || u != imageURL || digest != testDigest || v.method != "s3-readback" || v.run != testRun {
			t.Errorf("amd64 = %q, %q, %+v, %v", u, digest, v, err)
		}
		u, digest, v, err = namedIn(path, "os-image-arm64")
		if err != nil || u != arm || digest != strings.Repeat("c", 64) || v.method != "http-get" {
			t.Errorf("arm64 = %q, %q, %+v, %v", u, digest, v, err)
		}
	})

	t.Run("refuses a record it cannot rely on", func(t *testing.T) {
		good := readback(nil)
		cases := []struct {
			name     string
			record   any
			fragment string
		}{
			{"not JSON", "{", "not readable JSON"},
			{"a list", []any{}, "is not a nvkr.dev/hardware-e2e-substitutions/v1 record"},
			{"another schema", substitutionsRecord(map[string]any{"schema": "nvkr.dev/other/v1"}, replacement(good, nil)), "is not a nvkr.dev/hardware-e2e-substitutions/v1 record"},
			{"a build from the subject", substitutionsRecord(map[string]any{"artifactSource": "build-subject"}, replacement(good, nil)), "only a run of NVKR outputs"},
			{"no replacements field", map[string]any{"schema": "nvkr.dev/hardware-e2e-substitutions/v1", "artifactSource": "nvkr-outputs"}, "names 0 replacements"},
			{"replacements that are not a list", substitutionsRecord(map[string]any{"replaced": map[string]any{"slot": imageSlot}}), "names 0 replacements"},
			{"no replacement for the slot", substitutionsRecord(nil, replacement(good, map[string]any{"slot": "os-image-arm64"})), "names 0 replacements"},
			{"a replacement that is text", substitutionsRecord(nil, "junk", replacement(good, nil)), "not an object"},
			{"a replacement that is null", substitutionsRecord(nil, nil, replacement(good, nil)), "not an object"},
			{"a replacement that is a list", substitutionsRecord(nil, []any{replacement(good, nil)}, replacement(good, nil)), "not an object"},
			{"two replacements for the slot", substitutionsRecord(nil, replacement(good, nil), replacement(good, nil)), "names 2 replacements"},
			{"no URL", substitutionsRecord(nil, replacement(good, map[string]any{"to": missing{}})), "names no URL"},
			{"a URL that is not text", substitutionsRecord(nil, replacement(good, map[string]any{"to": 5})), "names no URL"},
			{"no digest", substitutionsRecord(nil, replacement(good, map[string]any{"sha256": missing{}})), "names no URL"},
			{"an upper-case digest", substitutionsRecord(nil, replacement(good, map[string]any{"sha256": strings.ToUpper(testDigest)})), "names no URL"},
			{"a digest that ends in a newline", substitutionsRecord(nil, replacement(good, map[string]any{"sha256": testDigest + "\n"})), "names no URL"},
			{"no verification", substitutionsRecord(nil, replacement(missing{}, nil)), "must be an object"},
			{"a verification it does not know", substitutionsRecord(nil, replacement(map[string]any{"method": "head-object"}, nil)), "verification method must be"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "substitutions.json")
				if text, isText := tc.record.(string); isText {
					if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
						t.Fatal(err)
					}
				} else {
					writeJSONFile(t, path, tc.record)
				}
				_, _, _, err := namedIn(path, imageSlot)
				assertCoded(t, err, nerrors.CodeUnsupported, tc.fragment)
				if nerrors.ExitCode(err) != 13 {
					t.Errorf("exit = %d, want 13", nerrors.ExitCode(err))
				}
			})
		}
	})

	t.Run("refuses text after the record", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "substitutions.json")
		data, _ := json.Marshal(substitutionsRecord(nil, replacement(readback(nil), nil)))
		if err := os.WriteFile(path, append(data, []byte(" {}")...), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, _, err := namedIn(path, imageSlot)
		assertCoded(t, err, nerrors.CodeUnsupported, "not readable JSON")
	})

	t.Run("refuses a record that is not UTF-8, even in a field it never reads", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "substitutions.json")
		data, _ := json.Marshal(substitutionsRecord(map[string]any{"note": "MARK"}, replacement(readback(nil), nil)))
		data = bytes.Replace(data, []byte("MARK"), []byte("\xff"), 1)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, _, err := namedIn(path, imageSlot)
		assertCoded(t, err, nerrors.CodeUnsupported, "not valid UTF-8")
	})

	t.Run("refuses a record that is not there", func(t *testing.T) {
		_, _, _, err := namedIn(filepath.Join(t.TempDir(), "absent.json"), imageSlot)
		assertCoded(t, err, nerrors.CodeUnsupported, "not readable JSON")
	})
}

func TestSubstitutionsCommand(t *testing.T) {
	run := func(t *testing.T, fetch fetcher, record any, slot string) (code int, stdout, stderr, out string) {
		t.Helper()
		dir := t.TempDir()
		path, out := filepath.Join(dir, "substitutions.json"), filepath.Join(dir, "check.json")
		if record != nil {
			writeJSONFile(t, path, record)
		}
		code, stdout, stderr = execNodeImage(t, fetch, "--slot", slot, "--substitutions", path, "--out", out)
		return code, stdout, stderr, out
	}

	t.Run("a read-back image is recorded and never fetched", func(t *testing.T) {
		fetch := newScripted()
		code, stdout, _, out := run(t, fetch, substitutionsRecord(nil, replacement(readback(nil), nil)), imageSlot)
		if code != 0 || len(fetch.calls) != 0 {
			t.Fatalf("exit = %d, calls = %d", code, len(fetch.calls))
		}
		containsAll(t, stdout, fmt.Sprintf("node image recorded, not fetched: %s, %d bytes", imageSlot, len(testImage)), "read back by "+testRun)
		if info, err := os.Stat(out); err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("record mode = %v, err = %v", info, err)
		}
		data, _ := os.ReadFile(out)
		v, _ := parseVerification(decoded(t, readback(nil)))
		want, _ := recorded(imageSlot, imageURL, testDigest, v)
		if string(data) != canonical(t, want) {
			t.Errorf("record:\n%s\nwant:\n%s", data, canonical(t, want))
		}
	})

	t.Run("an http-get image is fetched from the URL the record names", func(t *testing.T) {
		fetch := goodScript()
		code, stdout, _, out := run(t, fetch, substitutionsRecord(nil, replacement(map[string]any{"method": "http-get"}, nil)), imageSlot)
		if code != 0 || !reflect.DeepEqual(fetch.urls(), []string{imageURL, imageURL}) {
			t.Fatalf("exit = %d, urls = %v", code, fetch.urls())
		}
		containsAll(t, stdout, "node image ok: "+imageSlot)
		data, _ := os.ReadFile(out)
		var record map[string]any
		_ = json.Unmarshal(data, &record)
		if record["method"] != "http-get" || record["observedByThisRun"] != true || record["url"] != imageURL || record["sha256"] != testDigest {
			t.Errorf("record = %v", record)
		}
	})

	t.Run("each slot follows its own method", func(t *testing.T) {
		arm := "os-image-arm64"
		record := substitutionsRecord(nil,
			replacement(map[string]any{"method": "http-get"}, nil),
			replacement(readback(nil), map[string]any{"slot": arm, "sha256": strings.Repeat("c", 64)}))
		fetch := newScripted()
		code, _, _, out := run(t, fetch, record, arm)
		if code != 0 || len(fetch.calls) != 0 {
			t.Fatalf("exit = %d, calls = %d", code, len(fetch.calls))
		}
		data, _ := os.ReadFile(out)
		var written map[string]any
		_ = json.Unmarshal(data, &written)
		if written["slot"] != arm || written["method"] != "s3-readback" || written["sha256"] != strings.Repeat("c", 64) {
			t.Errorf("record = %v", written)
		}
	})

	t.Run("an http-get that serves other bytes fails and writes nothing", func(t *testing.T) {
		other := newQCOW2().image(payloadOf("other payload ", 64))
		fetch := newScripted(probeAnswer(200, ""), bodyAnswer(other))
		code, _, stderr, out := run(t, fetch, substitutionsRecord(nil, replacement(map[string]any{"method": "http-get"}, nil)), imageSlot)
		if code != 22 || !strings.Contains(stderr, "[NVKR-E-VERIFY-FAILED]") || pathExists(out) {
			t.Errorf("exit = %d, stderr = %s, record written = %v", code, stderr, pathExists(out))
		}
	})

	t.Run("a record it cannot rely on fails before any request and writes nothing", func(t *testing.T) {
		good := readback(nil)
		cases := map[string]any{
			"no verification":        substitutionsRecord(nil, replacement(missing{}, nil)),
			"an unknown method":      substitutionsRecord(nil, replacement(map[string]any{"method": "head-object"}, nil)),
			"zero bytes":             substitutionsRecord(nil, replacement(readback(map[string]any{"bytes": 0}), nil)),
			"a build from subject":   substitutionsRecord(map[string]any{"artifactSource": "build-subject"}, replacement(good, nil)),
			"no replacement":         substitutionsRecord(nil, replacement(good, map[string]any{"slot": "os-image-arm64"})),
			"a URL the lane refuses": substitutionsRecord(nil, replacement(good, map[string]any{"to": "http://artifacts.example.test/a.qcow2"})),
		}
		for name, record := range cases {
			t.Run(name, func(t *testing.T) {
				fetch := newScripted()
				code, _, stderr, out := run(t, fetch, record, imageSlot)
				if code != 13 || !strings.Contains(stderr, "[NVKR-E-UNSUPPORTED]") || len(fetch.calls) != 0 || pathExists(out) {
					t.Errorf("exit = %d, stderr = %s, calls = %d, record written = %v", code, stderr, len(fetch.calls), pathExists(out))
				}
			})
		}
	})

	t.Run("never replaces an existing output and reads nothing first", func(t *testing.T) {
		dir := t.TempDir()
		out := filepath.Join(dir, "check.json")
		if err := os.WriteFile(out, []byte("keep\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		fetch := newScripted()
		// The substitution record does not exist: refusing the output comes first.
		code, _, stderr := execNodeImage(t, fetch, "--slot", imageSlot, "--substitutions", filepath.Join(dir, "absent.json"), "--out", out)
		if got, _ := os.ReadFile(out); code != 47 || string(got) != "keep\n" || len(fetch.calls) != 0 {
			t.Errorf("exit = %d, content = %q, calls = %d", code, got, len(fetch.calls))
		}
		containsAll(t, stderr, "[NVKR-E-IMMUTABLE-VIOLATION]")
	})
}
