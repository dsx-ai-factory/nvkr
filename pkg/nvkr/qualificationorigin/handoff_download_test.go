// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package qualificationorigin

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

type handoffDownloadFixture struct {
	source         *SourceRun
	list           qualificationArtifactList
	archive        []byte
	location       string
	locationStatus int
	downloadStatus int
	contentLength  int64
	hook           func(string)
	readHook       func(string)
	closeHook      func(string)
	transportError map[string]error
	requests       []string
	closed         []string
}

func newHandoffDownloadFixture(t *testing.T) *handoffDownloadFixture {
	t.Helper()
	request, repo, run := sourceFixture()
	source, err := authenticateSourceResponse(t.Context(), request, repo, run)
	if err != nil {
		t.Fatal(err)
	}
	archive := handoffZIP(t, []zip.FileHeader{{Name: "handoff.json", Method: zip.Deflate}}, []byte(`{"handoff":"original"}`))
	expired := false
	return &handoffDownloadFixture{source: source, archive: archive,
		location:       "https://artifacts.blob.core.windows.net/archive?sig=download-secret",
		locationStatus: http.StatusFound, downloadStatus: http.StatusOK, contentLength: -1,
		transportError: map[string]error{},
		list: qualificationArtifactList{TotalCount: "1", Artifacts: []qualificationArtifact{{
			ID: "1234567890123456789", Name: "nvkr-qualification-1234-2", Size: json.Number("123"), Digest: handoffArchiveDigest(archive), Expired: &expired,
			WorkflowRun: qualificationArtifactRun{ID: "1234", RepositoryID: "123", HeadRepositoryID: "123", HeadSHA: request.Commit, HeadBranch: "main"},
		}}}}
}

func handoffArchiveDigest(raw []byte) string {
	hash := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(hash[:])
}

func handoffZIP(t *testing.T, headers []zip.FileHeader, contents []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, header := range headers {
		member, err := writer.CreateHeader(&header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := member.Write(contents); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func (fixture *handoffDownloadFixture) github(t *testing.T) *GitHub {
	t.Helper()
	g := NewGitHub("read-secret")
	g.client.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		deadline, ok := request.Context().Deadline()
		if !ok || time.Until(deadline) > 2*time.Minute || request.Method != http.MethodGet {
			t.Fatal("unbounded or mutating artifact request")
		}
		phase := "archive"
		status := fixture.downloadStatus
		body := fixture.archive
		header := http.Header{}
		length := fixture.contentLength
		if request.URL.Host == "api.github.com" {
			if request.Header.Get("Authorization") != "Bearer read-secret" || request.Header.Get("X-GitHub-Api-Version") != "2022-11-28" {
				t.Fatal("API request lost scoped read credential or version")
			}
			switch request.URL.Path {
			case "/repos/example/nvkr/actions/runs/1234/artifacts":
				phase, status, length = "list", http.StatusOK, -1
				if request.URL.RawQuery != "name=nvkr-qualification-1234-2&per_page=100" {
					t.Fatalf("unexpected artifact filter %q", request.URL.RawQuery)
				}
				var err error
				body, err = json.Marshal(fixture.list)
				if err != nil {
					t.Fatal(err)
				}
			case "/repos/example/nvkr/actions/artifacts/1234567890123456789/zip":
				phase, status, length, body = "location", fixture.locationStatus, -1, nil
				header.Set("Location", fixture.location)
			default:
				t.Fatal("artifact request reached an unexpected API path")
			}
		} else {
			if request.URL.String() != fixture.location || request.Header.Get("Authorization") != "" ||
				request.Header.Get("Cookie") != "" || request.Header.Get("Referer") != "" {
				t.Fatal("archive request changed destination or inherited credentials")
			}
			if status >= 300 && status < 400 {
				header.Set("Location", "https://unapproved.invalid/next?sig=second-secret")
			}
		}
		fixture.requests = append(fixture.requests, phase)
		if fixture.hook != nil {
			fixture.hook(phase)
		}
		if err := fixture.transportError[phase]; err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: status, ContentLength: length, Header: header,
			Body: &handoffTestBody{reader: bytes.NewReader(body), read: func() {
				if fixture.readHook != nil {
					fixture.readHook(phase)
				}
			}, close: func() {
				fixture.closed = append(fixture.closed, phase)
				if fixture.closeHook != nil {
					fixture.closeHook(phase)
				}
			}}}, nil
	})
	return g
}

type handoffTestBody struct {
	reader io.Reader
	read   func()
	close  func()
}

func (body *handoffTestBody) Read(p []byte) (int, error) {
	if body.read != nil {
		body.read()
	}
	return body.reader.Read(p)
}

func (body *handoffTestBody) Close() error {
	body.close()
	return nil
}

func TestQualificationArtifactName(t *testing.T) {
	for _, tc := range []struct {
		name    string
		locator RunLocator
		want    string
	}{
		{"exact", RunLocator{Repository: "example/nvkr", RunID: "1234", Attempt: "2"}, "nvkr-qualification-1234-2"},
		{"missing attempt", RunLocator{Repository: "example/nvkr", RunID: "1234"}, ""},
		{"alternate numeric form", RunLocator{Repository: "example/nvkr", RunID: "1234", Attempt: "02"}, ""},
		{"invalid repository", RunLocator{Repository: "../nvkr", RunID: "1234", Attempt: "2"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := QualificationArtifactName(tc.locator)
			if tc.want == "" {
				assertCode(t, err, e.CodeUsage)
			} else if err != nil || got != tc.want {
				t.Fatalf("artifact name = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestDownloadQualificationHandoffExactArchiveAndCredentialIsolation(t *testing.T) {
	for _, host := range []string{"artifacts.blob.core.windows.net", "results-receiver.actions.githubusercontent.com"} {
		t.Run(host, func(t *testing.T) {
			fixture := newHandoffDownloadFixture(t)
			fixture.location = "https://" + host + "/archive?sig=download-secret"
			fixture.contentLength = int64(len(fixture.archive))
			g := fixture.github(t)
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			address, err := url.Parse(fixture.location)
			if err != nil {
				t.Fatal(err)
			}
			jar.SetCookies(address, []*http.Cookie{{Name: "credential", Value: "cookie-secret"}})
			g.client.Jar = jar
			raw, err := g.DownloadQualificationHandoff(t.Context(), fixture.source)
			if err != nil || string(raw) != `{"handoff":"original"}` {
				t.Fatalf("download returned %q, %v", raw, err)
			}
			if strings.Join(fixture.requests, ",") != "list,location,archive" || strings.Join(fixture.closed, ",") != "list,location,archive" {
				t.Fatalf("unexpected request or close ownership: requests=%v closed=%v", fixture.requests, fixture.closed)
			}
			if err := fixture.source.Require(); err != nil {
				t.Fatalf("download closed its borrowed source: %v", err)
			}
		})
	}
}

func TestDownloadQualificationHandoffRejectsArtifactMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*qualificationArtifactList)
		code e.Code
	}{
		{"missing", func(l *qualificationArtifactList) { l.TotalCount = "0"; l.Artifacts = nil }, e.CodeProvenanceMissing},
		{"duplicate", func(l *qualificationArtifactList) {
			l.TotalCount = "2"
			l.Artifacts = append(l.Artifacts, l.Artifacts[0])
		}, e.CodeProvenanceMissing},
		{"count mismatch", func(l *qualificationArtifactList) { l.TotalCount = "2" }, e.CodeProvenanceMissing},
		{"wrong name", func(l *qualificationArtifactList) { l.Artifacts[0].Name = "nvkr-qualification-1234-1" }, e.CodeVerifyFailed},
		{"expired", func(l *qualificationArtifactList) { *l.Artifacts[0].Expired = true }, e.CodeVerifyFailed},
		{"missing expiry", func(l *qualificationArtifactList) { l.Artifacts[0].Expired = nil }, e.CodeVerifyFailed},
		{"wrong run", func(l *qualificationArtifactList) { l.Artifacts[0].WorkflowRun.ID = "1235" }, e.CodeVerifyFailed},
		{"wrong commit", func(l *qualificationArtifactList) { l.Artifacts[0].WorkflowRun.HeadSHA = strings.Repeat("b", 40) }, e.CodeVerifyFailed},
		{"wrong branch", func(l *qualificationArtifactList) { l.Artifacts[0].WorkflowRun.HeadBranch = "feature" }, e.CodeVerifyFailed},
		{"fork repository", func(l *qualificationArtifactList) { l.Artifacts[0].WorkflowRun.HeadRepositoryID = "124" }, e.CodeVerifyFailed},
		{"missing repository IDs", func(l *qualificationArtifactList) {
			l.Artifacts[0].WorkflowRun.RepositoryID = ""
			l.Artifacts[0].WorkflowRun.HeadRepositoryID = ""
		}, e.CodeVerifyFailed},
		{"invalid artifact ID", func(l *qualificationArtifactList) { l.Artifacts[0].ID = "0" }, e.CodeVerifyFailed},
		{"missing pin", func(l *qualificationArtifactList) { l.Artifacts[0].Digest = "" }, e.CodeUnpinnedInput},
		{"invalid pin", func(l *qualificationArtifactList) { l.Artifacts[0].Digest = "sha256:" + strings.Repeat("A", 64) }, e.CodeUnpinnedInput},
		{"missing size", func(l *qualificationArtifactList) { l.Artifacts[0].Size = "" }, e.CodeVerifyFailed},
		{"oversized", func(l *qualificationArtifactList) { l.Artifacts[0].Size = "134217729" }, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newHandoffDownloadFixture(t)
			tc.edit(&fixture.list)
			raw, err := fixture.github(t).DownloadQualificationHandoff(t.Context(), fixture.source)
			assertCode(t, err, tc.code)
			if raw != nil || strings.Join(fixture.requests, ",") != "list" {
				t.Fatalf("invalid metadata downloaded bytes: %d, %v", len(raw), fixture.requests)
			}
		})
	}
}

func TestDownloadQualificationHandoffRejectsRedirects(t *testing.T) {
	for _, location := range []string{
		"", "http://artifacts.blob.core.windows.net/archive", "/relative", "https://user:secret@artifacts.blob.core.windows.net/archive",
		"https://artifacts.blob.core.windows.net:443/archive", "https://artifacts.blob.core.windows.net/archive#fragment",
		"https://artifacts.blob.core.windows.net/archive#", "https://blob.core.windows.net/archive", "https://.blob.core.windows.net/archive",
		"https://evilblob.core.windows.net/archive", "https://artifacts.blob.core.windows.net.attacker.invalid/archive",
		"https://results-receiver.actions.githubusercontent.com.attacker.invalid/archive", "https://a..blob.core.windows.net/archive",
	} {
		t.Run(location, func(t *testing.T) {
			fixture := newHandoffDownloadFixture(t)
			fixture.location = location
			raw, err := fixture.github(t).DownloadQualificationHandoff(t.Context(), fixture.source)
			assertCode(t, err, e.CodeVerifyFailed)
			if raw != nil || strings.Join(fixture.requests, ",") != "list,location" {
				t.Fatal("unapproved redirect reached archive transport")
			}
		})
	}
	for _, tc := range []struct {
		name               string
		location, download int
	}{
		{"API success without redirect", http.StatusOK, http.StatusOK},
		{"API permanent redirect", http.StatusMovedPermanently, http.StatusOK},
		{"expired download", http.StatusGone, http.StatusOK},
		{"second redirect", http.StatusFound, http.StatusFound},
		{"download failure", http.StatusFound, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newHandoffDownloadFixture(t)
			fixture.locationStatus, fixture.downloadStatus = tc.location, tc.download
			raw, err := fixture.github(t).DownloadQualificationHandoff(t.Context(), fixture.source)
			assertCode(t, err, e.CodeFetchFailed)
			if raw != nil {
				t.Fatal("failed download returned partial bytes")
			}
		})
	}
}

func TestDownloadQualificationHandoffRejectsArchiveSubstitution(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*handoffDownloadFixture)
	}{
		{"wrong archive pin", func(f *handoffDownloadFixture) { f.list.Artifacts[0].Digest = "sha256:" + strings.Repeat("b", 64) }},
		{"too large response", func(f *handoffDownloadFixture) { f.contentLength = maxQualificationArchiveBytes + 1 }},
		{"truncated response", func(f *handoffDownloadFixture) { f.contentLength = int64(len(f.archive)) + 1 }},
		{"invalid ZIP", func(f *handoffDownloadFixture) {
			f.archive = []byte("not an archive")
			f.list.Artifacts[0].Digest = handoffArchiveDigest(f.archive)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newHandoffDownloadFixture(t)
			tc.edit(fixture)
			raw, err := fixture.github(t).DownloadQualificationHandoff(t.Context(), fixture.source)
			assertCode(t, err, e.CodeVerifyFailed)
			if raw != nil || len(fixture.closed) != len(fixture.requests) {
				t.Fatal("failed archive returned bytes or leaked a response body")
			}
		})
	}
}

func TestQualificationHandoffZIPMembership(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers []zip.FileHeader
	}{
		{"empty", nil},
		{"extra", []zip.FileHeader{{Name: "handoff.json"}, {Name: "extra.json"}}},
		{"duplicate", []zip.FileHeader{{Name: "handoff.json"}, {Name: "handoff.json"}}},
		{"traversal", []zip.FileHeader{{Name: "../handoff.json"}}},
		{"absolute", []zip.FileHeader{{Name: "/handoff.json"}}},
		{"nested", []zip.FileHeader{{Name: "folder/handoff.json"}}},
		{"directory", []zip.FileHeader{{Name: "handoff.json/"}}},
		{"symlink", []zip.FileHeader{func() zip.FileHeader {
			h := zip.FileHeader{Name: "handoff.json"}
			h.SetMode(os.ModeSymlink | 0o777)
			return h
		}()}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newHandoffDownloadFixture(t)
			contents := []byte("original")
			if tc.name == "directory" {
				contents = nil
			}
			fixture.archive = handoffZIP(t, tc.headers, contents)
			fixture.list.Artifacts[0].Digest = handoffArchiveDigest(fixture.archive)
			raw, err := fixture.github(t).DownloadQualificationHandoff(t.Context(), fixture.source)
			assertCode(t, err, e.CodeVerifyFailed)
			if raw != nil {
				t.Fatal("invalid ZIP returned bytes")
			}
		})
	}
	for _, tc := range []struct {
		name    string
		size    uint32
		corrupt bool
	}{
		{"declared oversize", MaxQualificationHandoffBytes + 1, false},
		{"content size mismatch", 200, false},
		{"corrupt contents", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			archive := handoffZIP(t, []zip.FileHeader{{Name: "handoff.json", Method: zip.Store}}, []byte("original"))
			central := bytes.Index(archive, []byte{'P', 'K', 1, 2})
			if central < 0 {
				t.Fatal("fixture has no central directory")
			}
			if tc.corrupt {
				archive[30+len("handoff.json")] ^= 1
			} else {
				binary.LittleEndian.PutUint32(archive[central+24:central+28], tc.size)
			}
			raw, err := unpackQualificationHandoff(t.Context(), archive)
			assertCode(t, err, e.CodeVerifyFailed)
			if raw != nil {
				t.Fatal("corrupt member returned bytes")
			}
		})
	}
}

func TestDownloadQualificationHandoffAdmissionAndRevocation(t *testing.T) {
	for _, tc := range []struct {
		name, phase string
		onClose     bool
	}{
		{"nil source", "nil", false}, {"unissued source", "unissued", false}, {"closed source", "before", false},
		{"during list", "list", false}, {"during location", "location", false}, {"during download", "archive", false},
		{"after download", "archive", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newHandoffDownloadFixture(t)
			source := fixture.source
			switch tc.phase {
			case "nil":
				source = nil
			case "unissued":
				source = &SourceRun{}
			case "before":
				_ = source.Close()
			default:
				revoke := func(phase string) {
					if phase == tc.phase {
						_ = source.Close()
					}
				}
				if tc.onClose {
					fixture.closeHook = revoke
				} else {
					fixture.hook = revoke
				}
			}
			raw, err := fixture.github(t).DownloadQualificationHandoff(t.Context(), source)
			assertCode(t, err, e.CodeProvenanceMissing)
			if raw != nil || (tc.phase == "nil" || tc.phase == "unissued" || tc.phase == "before") && len(fixture.requests) != 0 {
				t.Fatal("invalid source returned bytes or caused admission I/O")
			}
		})
	}
}

func TestDownloadQualificationHandoffCancellation(t *testing.T) {
	for _, phase := range []string{"before", "list", "location", "archive"} {
		t.Run(phase, func(t *testing.T) {
			fixture := newHandoffDownloadFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if phase == "before" {
				cancel()
			} else {
				fixture.hook = func(current string) {
					if current == phase {
						cancel()
					}
				}
			}
			raw, err := fixture.github(t).DownloadQualificationHandoff(ctx, fixture.source)
			assertCode(t, err, e.CodeTimeout)
			if raw != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation returned bytes or lost cause: %v", err)
			}
			if phase == "before" && len(fixture.requests) != 0 {
				t.Fatal("canceled admission reached transport")
			}
		})
	}
	fixture := newHandoffDownloadFixture(t)
	_, err := fixture.github(t).DownloadQualificationHandoff(nil, fixture.source) //nolint:staticcheck // Nil-context refusal is a public contract.
	assertCode(t, err, e.CodeUsage)
	if len(fixture.requests) != 0 {
		t.Fatal("nil context reached transport")
	}
}

func TestDownloadQualificationHandoffDoesNotExposeSignedLocation(t *testing.T) {
	for _, phase := range []string{"location", "archive"} {
		t.Run(phase, func(t *testing.T) {
			fixture := newHandoffDownloadFixture(t)
			fixture.transportError[phase] = &url.Error{Op: "Get", URL: fixture.location, Err: errors.New("failed with read-secret and download-secret")}
			raw, err := fixture.github(t).DownloadQualificationHandoff(t.Context(), fixture.source)
			assertCode(t, err, e.CodeFetchFailed)
			if raw != nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), fixture.location) {
				t.Fatal("transport error exposed credentials or returned partial bytes")
			}
			var transportErr *url.Error
			if errors.As(err, &transportErr) {
				t.Fatal("error chain retained credential-bearing URL")
			}
		})
	}
}

// These tests observe the complete standard ZIP parser boundary, not only the
// reader wrapper, so moving the budget after NewReader would regress them.
func TestQualificationHandoffBoundsZIPMetadataBeforeMembership(t *testing.T) {
	headers := make([]zip.FileHeader, 4000)
	for i := range headers {
		headers[i] = zip.FileHeader{Name: "handoff.json", Comment: strings.Repeat("x", 512)}
	}
	archive := handoffZIP(t, headers, nil)
	source := &handoffCountedReaderAt{reader: bytes.NewReader(archive)}
	raw, err := readQualificationHandoffArchive(t.Context(), source, int64(len(archive)))
	assertCode(t, err, e.CodeVerifyFailed)
	if raw != nil || !strings.Contains(err.Error(), "metadata exceeds its read bound") || source.bytes > maxQualificationZIPMetadataBytes {
		t.Fatalf("metadata parsing was not bounded before membership: bytes=%d error=%v", source.bytes, err)
	}
}

func TestQualificationHandoffZIPParsingHonorsCancellation(t *testing.T) {
	archive := handoffZIP(t, []zip.FileHeader{{Name: "handoff.json", Method: zip.Store}}, bytes.Repeat([]byte("x"), 128<<10))
	for _, phase := range []string{"before metadata", "during metadata", "opening member"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			source := &handoffCountedReaderAt{reader: bytes.NewReader(archive)}
			if phase == "before metadata" {
				cancel()
			}
			source.afterRead = func(offset int64) {
				if phase == "during metadata" || phase == "opening member" && offset == 0 {
					cancel()
				}
			}
			raw, err := readQualificationHandoffArchive(ctx, source, int64(len(archive)))
			assertCode(t, err, e.CodeTimeout)
			if raw != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled parser returned bytes or lost cause: %v", err)
			}
			if phase == "before metadata" && source.bytes != 0 {
				t.Fatal("canceled parser read metadata")
			}
		})
	}
}

func TestQualificationHandoffZIPMetadataBudgetDoesNotLimitPayload(t *testing.T) {
	original := bytes.Repeat([]byte("x"), 2*maxQualificationZIPMetadataBytes)
	archive := handoffZIP(t, []zip.FileHeader{{Name: "handoff.json", Method: zip.Store}}, original)
	raw, err := unpackQualificationHandoff(t.Context(), archive)
	if err != nil || !bytes.Equal(raw, original) {
		t.Fatalf("valid bounded payload hit metadata limit: %v", err)
	}
}

type handoffCountedReaderAt struct {
	reader    io.ReaderAt
	bytes     int
	afterRead func(int64)
}

func (reader *handoffCountedReaderAt) ReadAt(p []byte, offset int64) (int, error) {
	n, err := reader.reader.ReadAt(p, offset)
	reader.bytes += len(p)
	if reader.afterRead != nil {
		reader.afterRead(offset)
	}
	return n, err
}
