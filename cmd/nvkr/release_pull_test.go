// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dsx-ai-factory/nvkr/internal/runpresenter"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/urfave/cli/v2"
)

func TestReleasePullDownloadsOnlySelectedOutput(t *testing.T) {
	for _, selected := range []struct {
		arch, output, path, payload string
		mode                        os.FileMode
	}{
		{"amd64", "tools", "cli", "opaque cli fixture bytes", 0o755},
		{"arm64", "tools", "cli", "opaque cli fixture bytes", 0o755},
		{"amd64", "node-image", "node-image.qcow2", "opaque disk fixture bytes", 0o644},
		{"arm64", "node-image", "node-image.qcow2", "opaque disk fixture bytes", 0o644},
	} {
		t.Run(selected.arch+"/"+selected.output, func(t *testing.T) {
			arch := selected.arch
			fixture := releaseBrowsePayloadFixture(t, arch)
			var selectedGets, otherGets atomic.Int64
			var corruptMetadata atomic.Bool
			handler := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					if fixture.payloads[filepath.Base(r.URL.Path)] && strings.Contains(r.URL.Path, "/blobs/") {
						if strings.Contains(r.URL.Path, "/product/"+selected.output+"/") {
							selectedGets.Add(1)
						} else {
							otherGets.Add(1)
							http.Error(w, "unselected payload must not be downloaded", http.StatusForbidden)
							return
						}
					}
					if corruptMetadata.Load() && strings.HasSuffix(r.URL.Path, "/blobs/"+fixture.diskConfig) {
						raw := append([]byte(nil), fixture.diskConfigBytes...)
						raw[len(raw)-1] ^= 1
						_, _ = w.Write(raw)
						return
					}
				}
				handler.ServeHTTP(w, r)
			}))
			defer server.Close()
			transport := delivery.RegistryOptions{Anonymous: true, PlainHTTP: true}
			reference, err := delivery.Push(t.Context(), fixture.layout, strings.TrimPrefix(server.URL, "http://")+"/product/releases", transport)
			if err != nil {
				t.Fatal(err)
			}
			selectedGets.Store(0)
			otherGets.Store(0)
			root := t.TempDir()
			out, workspace := filepath.Join(root, selected.output), filepath.Join(root, "workspace")
			args := []string{"--reference", reference, "--output", selected.output, "--out", out, "--workspace", workspace, "--anonymous", "--plain-http"}
			raw, err := runReleasePull(t.Context(), args...)
			if err != nil {
				t.Fatal(err)
			}
			if selectedGets.Load() == 0 || otherGets.Load() != 0 {
				t.Fatalf("wrong payloads fetched: selected=%d other=%d", selectedGets.Load(), otherGets.Load())
			}
			payload, err := os.ReadFile(filepath.Join(out, selected.path))
			if err != nil || string(payload) != selected.payload {
				t.Fatalf("wrong extracted bytes: %q, %v", payload, err)
			}
			info, err := os.Stat(filepath.Join(out, selected.path))
			if err != nil || info.Mode().Perm() != selected.mode {
				t.Fatalf("payload mode not retained: %v, %v", info, err)
			}
			for _, want := range []string{filepath.Join(out, selected.path), "linux/" + arch, "payload integrity checked", "signatures and policy not verified"} {
				if !strings.Contains(raw, want) {
					t.Errorf("pull result missing %q: %s", want, raw)
				}
			}
			assertPullWorkspaceEmpty(t, workspace)
			// The same real delivery owner, presented as JSON: every reported path,
			// digest and size must describe the file actually written.
			jsonOut := filepath.Join(root, "json-"+selected.output)
			rawJSON, err := runReleasePull(t.Context(), append(args[:5:5], jsonOut, "--workspace", workspace, "--anonymous", "--plain-http", "--format", "json")...)
			if err != nil {
				t.Fatal(err)
			}
			var pulled struct {
				Output struct {
					Name, Kind, OS, Arch string
				}
				Destination, Payload string
				Files                []struct {
					Path, SHA256 string
					Size         int64
				}
			}
			dec := json.NewDecoder(strings.NewReader(rawJSON))
			if err := dec.Decode(&pulled); err != nil {
				t.Fatalf("decoding JSON pull: %v: %s", err, rawJSON)
			}
			if pulled.Output.Name != selected.output || pulled.Output.OS != "linux" || pulled.Output.Arch != arch ||
				pulled.Destination != jsonOut || pulled.Payload != "files" || len(pulled.Files) != 1 {
				t.Fatalf("unexpected JSON pull result: %s", rawJSON)
			}
			written, err := os.ReadFile(filepath.Join(jsonOut, pulled.Files[0].Path))
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(written)
			if pulled.Files[0].Path != selected.path || pulled.Files[0].SHA256 != "sha256:"+hex.EncodeToString(sum[:]) ||
				pulled.Files[0].Size != int64(len(written)) {
				t.Errorf("JSON file record does not describe the written file: %+v (sha256:%x, %d bytes)", pulled.Files[0], sum, len(written))
			}
			assertPullWorkspaceEmpty(t, workspace)
			for _, test := range []struct {
				name, output string
				corrupt      bool
				code         e.Code
			}{
				{"unknown", "missing", false, e.CodeUsage},
				{"invalid metadata", selected.output, true, e.CodeFetchFailed},
			} {
				t.Run(test.name, func(t *testing.T) {
					corruptMetadata.Store(test.corrupt)
					defer corruptMetadata.Store(false)
					selectedGets.Store(0)
					otherGets.Store(0)
					destination := filepath.Join(root, test.name)
					raw, err := runReleasePull(t.Context(), "--reference", reference, "--output", test.output, "--out", destination, "--workspace", workspace, "--anonymous", "--plain-http")
					if cliErrorCode(err) != test.code || raw != "" {
						t.Fatalf("failed selection returned output or wrong code: %q, %v", raw, err)
					}
					if selectedGets.Load() != 0 || otherGets.Load() != 0 {
						t.Fatal("failed selection downloaded payloads")
					}
					if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("failed selection wrote destination: %v", err)
					}
					assertPullWorkspaceEmpty(t, workspace)
				})
			}
		})
	}
}

func TestReleasePullRejectsInvalidUsageBeforeAcquisition(t *testing.T) {
	for _, test := range []struct {
		name  string
		flags []string
		code  e.Code
	}{
		{"missing output", nil, e.CodeUsage},
		{"empty output", []string{"--output", ""}, e.CodeUsage},
		{"timeout", []string{"--output", "tools", "--timeout", "0s"}, e.CodeUsage},
		{"positional", []string{"--output", "tools", "unexpected"}, e.CodeUsage},
		{"tag", []string{"--output", "tools", "--reference", "registry.example.test/releases:latest"}, e.CodeUnverifiedRef},
		{"empty destination", []string{"--output", "tools", "--out", ""}, e.CodeUsage},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			args := []string{"--reference", "127.0.0.1:1/product/releases@sha256:" + strings.Repeat("a", 64), "--workspace", filepath.Join(root, "workspace"), "--out", filepath.Join(root, "out")}
			raw, err := runReleasePull(t.Context(), append(args, test.flags...)...)
			if cliErrorCode(err) != test.code {
				t.Fatalf("invalid usage returned wrong code: %v; output=%q", err, raw)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed admission touched filesystem: %v, %v", entries, err)
			}
		})
	}
}

func TestReleasePullEscapesRegistryLabels(t *testing.T) {
	for _, control := range []string{"\x1b[2J", "\u202e", "\t"} {
		t.Run(control, func(t *testing.T) {
			result := delivery.PulledOutput{Output: delivery.PublicationOutput{Output: delivery.Output{Kind: "executable" + control, OS: "linux" + control, Arch: "arm64" + control}},
				Files: []delivery.PulledFile{{Path: "cli" + control, SHA256: "sha256:" + strings.Repeat("a", 64)}}}
			raw := releasePullMessage("tools"+control, "out"+control, result)
			if strings.Contains(raw, control) {
				t.Fatalf("pull printed a terminal control character: %q", raw)
			}
		})
	}
}

func runReleasePull(ctx context.Context, flags ...string) (string, error) {
	var out bytes.Buffer
	app := &cli.App{Commands: []*cli.Command{cmdRelease()}, Writer: &out}
	err := app.RunContext(ctx, append([]string{"nvkr", "release", "pull"}, flags...))
	return out.String(), err
}

func assertPullWorkspaceEmpty(t *testing.T, workspace string) {
	t.Helper()
	entries, err := os.ReadDir(workspace)
	if err != nil || len(entries) != 0 {
		t.Fatalf("pull leaked workspace: %v, %v", entries, err)
	}
}

func TestReleasePullPresentationSurroundsOneOwnedOperation(t *testing.T) {
	for _, test := range []struct {
		name                 string
		eligible             bool
		ownerCode            e.Code
		wantCode             e.Code
		wantCalls, wantViews int
	}{
		{"success", true, "", "", 1, 1},
		{"failure", true, e.CodeFetchFailed, e.CodeFetchFailed, 1, 1},
		{"ineligible terminal", false, "", e.CodeUsage, 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls, views := 0, 0
			destination := filepath.Join(t.TempDir(), "output")
			var out bytes.Buffer
			operation := func(ctx context.Context, reference, output, path string, options lifecycle.PullOptions) (delivery.PulledOutput, error) {
				calls++
				if ctx == nil || reference != "exact-reference" || output != "tools" || path != destination || options.Timeout <= 0 {
					t.Fatal("command lost selected operation inputs")
				}
				if test.ownerCode != "" {
					return delivery.PulledOutput{}, e.New(test.ownerCode, "private-error-sentinel")
				}
				if err := os.Mkdir(destination, 0o700); err != nil {
					t.Fatal(err)
				}
				return delivery.PulledOutput{Output: delivery.PublicationOutput{Output: delivery.Output{Kind: "executable", OS: "linux", Arch: "amd64"}}, Files: []delivery.PulledFile{{Path: "cli", SHA256: "sha256:" + strings.Repeat("a", 64), Size: 17}}}, nil
			}
			app := &cli.App{Writer: &out, Commands: []*cli.Command{{Name: "release", Subcommands: []*cli.Command{releasePullCmdWithOperation(operation)}}}, Metadata: map[string]interface{}{commandPresentationServicesKey: runPresentationServices{
				eligible: func(io.Writer) bool { return test.eligible },
				show: func(_ context.Context, options runpresenter.Options, source runSnapshotSource, work func(context.Context) error) runpresenter.Result {
					views++
					if calls != 1 || options.Live || work != nil {
						t.Fatal("viewer started before operation returned or offered live work")
					}
					snapshot := source.Snapshot()
					if snapshot.FailureCode != string(test.ownerCode) {
						t.Fatal("viewer lost operation error")
					}
					if test.ownerCode == "" {
						if _, err := os.Stat(destination); err != nil {
							t.Fatal("viewer preceded destination publication", err)
						}
						if snapshot.Status != "complete" || len(snapshot.Items) != 1 || snapshot.Items[0].Name != "cli" {
							t.Fatal("viewer lost written files")
						}
					}
					return runpresenter.Result{}
				},
			}}}
			err := app.RunContext(t.Context(), []string{"nvkr", "release", "pull", "--reference", "exact-reference", "--output", "tools", "--out", destination, "--ui=tui"})
			if cliErrorCode(err) != test.wantCode || calls != test.wantCalls || views != test.wantViews || out.Len() != 0 {
				t.Fatalf("unexpected result: calls=%d views=%d output=%q err=%v", calls, views, out.String(), err)
			}
		})
	}
}

func runInjectedReleasePull(t *testing.T, result delivery.PulledOutput, opErr error, calls *int, flags ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := runInjectedReleasePullTo(t, &out, result, opErr, calls, flags...)
	return out.String(), err
}

func runInjectedReleasePullTo(t *testing.T, w io.Writer, result delivery.PulledOutput, opErr error, calls *int, flags ...string) error {
	t.Helper()
	operation := func(context.Context, string, string, string, lifecycle.PullOptions) (delivery.PulledOutput, error) {
		*calls++
		return result, opErr
	}
	// An eligible TUI makes --ui=tui reach the JSON conflict check instead of
	// failing earlier on terminal eligibility; the viewer must never open.
	services := runPresentationServices{eligible: func(io.Writer) bool { return true },
		show: func(context.Context, runpresenter.Options, runSnapshotSource, func(context.Context) error) runpresenter.Result {
			t.Error("TUI opened for a JSON or rejected request")
			return runpresenter.Result{}
		}}
	app := &cli.App{Writer: w, Metadata: map[string]interface{}{commandPresentationServicesKey: services},
		Commands: []*cli.Command{{Name: "release", Subcommands: []*cli.Command{releasePullCmdWithOperation(operation)}}}}
	args := append([]string{"nvkr", "release", "pull", "--reference", "exact-reference", "--output", "tools", "--out", "dest"}, flags...)
	return app.RunContext(t.Context(), args)
}

func TestReleasePullJSONResult(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	buildDigest := "sha256:" + strings.Repeat("b", 64)
	fileDigest := "sha256:" + strings.Repeat("c", 64)
	scope := `"inspectionDetails":{"checks":{"metadataIntegrity":"checked","payloadIntegrity":"checked",` +
		`"signatures":"not-checked","consumerPolicy":"not-checked","registryAvailability":"not-checked",` +
		`"installationCompatibility":"not-checked"}}`
	// Fields owned by delivery but outside the script contract are populated so
	// an accidental leak into the JSON fails the exact comparison below.
	publication := func(output delivery.Output) delivery.PublicationOutput {
		return delivery.PublicationOutput{Output: output, ArtifactType: "internal/artifact-type",
			DescriptorDigest: "sha256:" + strings.Repeat("d", 64), ChartArchiveDigest: "sha256:" + strings.Repeat("e", 64)}
	}
	head := `{"schemaVersion":"nvkr.dev/release-pull-result/v1","reference":"exact-reference",`
	for _, test := range []struct {
		name   string
		result delivery.PulledOutput
		want   string
	}{
		{"executable", delivery.PulledOutput{
			Output:  publication(delivery.Output{Name: "tools", Kind: "executable", OS: "linux", Arch: "amd64", Digest: digest, BuildDigest: buildDigest}),
			Payload: delivery.PulledPayloadFiles,
			Files:   []delivery.PulledFile{{Path: "cli", SHA256: fileDigest, Size: 17}}},
			head + `"output":{"name":"tools","kind":"executable","digest":"` + digest + `","buildDigest":"` + buildDigest +
				`","os":"linux","arch":"amd64"},"destination":"dest","payload":"files",` +
				`"files":[{"path":"cli","sha256":"` + fileDigest + `","size":17}],` + scope + `}`},
		{"chart", delivery.PulledOutput{
			Output:  publication(delivery.Output{Name: "tools", Kind: "helm-oci", Digest: digest, BuildDigest: buildDigest}),
			Payload: delivery.PulledPayloadFiles,
			Files:   []delivery.PulledFile{{Path: "tools.tgz", SHA256: fileDigest, Size: 99}}},
			head + `"output":{"name":"tools","kind":"helm-oci","digest":"` + digest + `","buildDigest":"` + buildDigest +
				`"},"destination":"dest","payload":"files",` +
				`"files":[{"path":"tools.tgz","sha256":"` + fileDigest + `","size":99}],` + scope + `}`},
		// For a disk image Output.Digest names the observed deliverable while the
		// file record carries the raw QCOW2 digest; the two must stay distinct.
		{"disk image", delivery.PulledOutput{
			Output:  publication(delivery.Output{Name: "tools", Kind: "disk-image", OS: "linux", Arch: "arm64", Digest: digest, BuildDigest: buildDigest}),
			Payload: delivery.PulledPayloadFiles,
			Files:   []delivery.PulledFile{{Path: "tools.qcow2", SHA256: fileDigest, Size: 4096}}},
			head + `"output":{"name":"tools","kind":"disk-image","digest":"` + digest + `","buildDigest":"` + buildDigest +
				`","os":"linux","arch":"arm64"},"destination":"dest","payload":"files",` +
				`"files":[{"path":"tools.qcow2","sha256":"` + fileDigest + `","size":4096}],` + scope + `}`},
		{"oci directory", delivery.PulledOutput{
			Output: publication(delivery.Output{Name: "tools", Kind: "oci", Digest: digest, BuildDigest: buildDigest,
				ImageIndex: &delivery.ImageIndexBinding{RequiredPlatforms: []string{"linux/amd64", "linux/arm64"}}}),
			Payload: delivery.PulledPayloadDirectory},
			head + `"output":{"name":"tools","kind":"oci","digest":"` + digest + `","buildDigest":"` + buildDigest +
				`","imageIndex":{"requiredPlatforms":["linux/amd64","linux/arm64"]}},"destination":"dest","payload":"directory",` +
				`"files":[],` + scope + `}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			raw, err := runInjectedReleasePull(t, test.result, nil, &calls, "--format", "json")
			if err != nil || calls != 1 {
				t.Fatalf("pull failed: calls=%d err=%v", calls, err)
			}
			var compact bytes.Buffer
			decoder := json.NewDecoder(strings.NewReader(raw))
			var value json.RawMessage
			if err := decoder.Decode(&value); err != nil {
				t.Fatalf("output is not a JSON value: %v: %q", err, raw)
			}
			var extra json.RawMessage
			if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
				t.Fatalf("output is not exactly one JSON value: trailing decode=%v: %q", err, raw)
			}
			if err := json.Compact(&compact, value); err != nil {
				t.Fatal(err)
			}
			if compact.String() != test.want {
				t.Errorf("result mismatch\n got: %s\nwant: %s", compact.String(), test.want)
			}
		})
	}
}

func TestReleasePullJSONAdmissionAndFailure(t *testing.T) {
	for _, test := range []struct {
		name      string
		flags     []string
		opErr     error
		wantCode  e.Code
		wantCalls int
		wantMsg   string
	}{
		{"unknown format", []string{"--format", "yaml"}, nil, e.CodeUsage, 0, "must be text or json, got \"yaml\""},
		{"json with tui", []string{"--format", "json", "--ui=tui"}, nil, e.CodeUsage, 0, "cannot be combined"},
		{"operation error", []string{"--format", "json"}, e.New(e.CodeFetchFailed, "owner failure"), e.CodeFetchFailed, 1, "owner failure"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			raw, err := runInjectedReleasePull(t, delivery.PulledOutput{}, test.opErr, &calls, test.flags...)
			if cliErrorCode(err) != test.wantCode || calls != test.wantCalls || raw != "" {
				t.Fatalf("unexpected: code=%v calls=%d output=%q err=%v", cliErrorCode(err), calls, raw, err)
			}
			if err == nil || !strings.Contains(err.Error(), test.wantMsg) {
				t.Fatalf("error %v does not carry the expected reason %q", err, test.wantMsg)
			}
		})
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestReleasePullJSONWriteFailure(t *testing.T) {
	calls := 0
	result := delivery.PulledOutput{Output: delivery.PublicationOutput{Output: delivery.Output{Name: "tools", Kind: "helm-oci"}},
		Payload: delivery.PulledPayloadFiles}
	err := runInjectedReleasePullTo(t, failingWriter{}, result, nil, &calls, "--format", "json")
	if cliErrorCode(err) != e.CodeUnavailable || calls != 1 {
		t.Fatalf("unexpected: code=%v calls=%d err=%v", cliErrorCode(err), calls, err)
	}
}

// An explicit JSON format keeps stdout machine-readable even when --ui=auto
// would otherwise open the TUI on an eligible terminal.
func TestReleasePullJSONWithAutoUIStaysPlain(t *testing.T) {
	calls := 0
	result := delivery.PulledOutput{Output: delivery.PublicationOutput{Output: delivery.Output{Name: "tools", Kind: "helm-oci"}},
		Payload: delivery.PulledPayloadFiles}
	raw, err := runInjectedReleasePull(t, result, nil, &calls, "--format", "json", "--ui=auto")
	if err != nil || calls != 1 || !json.Valid([]byte(raw)) {
		t.Fatalf("unexpected: calls=%d err=%v output=%q", calls, err, raw)
	}
}

func TestReleasePullJSONRejectsUnclassifiedPayloadWithoutOutput(t *testing.T) {
	calls := 0
	result := delivery.PulledOutput{Output: delivery.PublicationOutput{Output: delivery.Output{Name: "tools", Kind: "future-format"}}}
	raw, err := runInjectedReleasePull(t, result, nil, &calls, "--format", "json")
	if cliErrorCode(err) != e.CodeInternal || raw != "" {
		t.Fatalf("unclassified payload produced output=%q err=%v", raw, err)
	}
}

func TestReleasePullTextDefaultUnchanged(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	result := delivery.PulledOutput{Output: delivery.PublicationOutput{Output: delivery.Output{Name: "tools", Kind: "executable", OS: "linux", Arch: "amd64"}},
		Files: []delivery.PulledFile{{Path: "cli", SHA256: digest, Size: 1}}}
	// The literal pins the pre-existing bytes; it must not be derived from the
	// production formatter.
	want := "Pulled \"tools\" (\"executable\") into \"dest\"; payload integrity checked, signatures and policy not verified.\n" +
		"  Platform: \"linux/amd64\"\n" +
		"  \"dest/cli\" (" + digest + ")\n"
	for _, flags := range [][]string{nil, {"--format", "text"}} {
		calls := 0
		raw, err := runInjectedReleasePull(t, result, nil, &calls, flags...)
		if err != nil || calls != 1 || raw != want {
			t.Fatalf("text bytes changed with flags %v: %q, calls=%d, err=%v", flags, raw, calls, err)
		}
	}
}

func TestReleasePullJSONEdgeFiles(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	result := delivery.PulledOutput{Output: delivery.PublicationOutput{Output: delivery.Output{Name: "tools", Kind: "executable"}},
		Payload: delivery.PulledPayloadFiles,
		Files:   []delivery.PulledFile{{Path: "empty", SHA256: digest, Size: 0}, {Path: "bin/cli", SHA256: digest, Size: 3}}}
	calls := 0
	raw, err := runInjectedReleasePull(t, result, nil, &calls, "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Files []struct {
			Path string `json:"path"`
			Size *int64 `json:"size"`
		} `json:"files"`
	}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatal(err)
	}
	// A legitimately empty file keeps an explicit size of 0, and order is the
	// owner's returned order, not re-sorted or deduplicated by the presenter.
	if len(decoded.Files) != 2 || decoded.Files[0].Path != "empty" || decoded.Files[0].Size == nil || *decoded.Files[0].Size != 0 ||
		decoded.Files[1].Path != "bin/cli" {
		t.Fatalf("file records changed: %s", raw)
	}
}
