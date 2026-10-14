// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/pipeline"
)

func executableReceiptFixture(t *testing.T) (*ArtifactCheckReceipt, ExecutableMemberDescription) {
	t.Helper()
	request, consumer, verifier := newQualificationFixture(t)
	receipt, _, err := consumer.checkQualification(t.Context(), request, verifier.source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receipt.Close() })
	description, err := receipt.Description()
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range description.Outputs {
		if output.Kind == OutputExecutable {
			bundle, err := receipt.lifetime.verified.publication.ExecutableBundle(output.Name)
			if err != nil {
				t.Fatal(err)
			}
			entry := bundle.Entries[0]
			return receipt, ExecutableMemberDescription{Output: output.Name, Member: entry.Name, Platform: bundle.OS + "/" + bundle.Arch, PayloadDigest: entry.SHA256, Size: entry.Size}
		}
	}
	t.Fatal("fixture has no executable member")
	return nil, ExecutableMemberDescription{}
}

func TestArtifactExecutableMemberAdmissionAndExactBytes(t *testing.T) {
	receipt, member := executableReceiptFixture(t)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	for _, tc := range []struct {
		name                        string
		receipt                     *ArtifactCheckReceipt
		ctx                         context.Context
		output, component, platform string
		limit                       int64
		nilWriter                   bool
		code                        e.Code
	}{
		{"nil receipt", nil, t.Context(), member.Output, member.Member, member.Platform, member.Size, false, e.CodeGateUnmet},
		{"zero receipt", &ArtifactCheckReceipt{}, t.Context(), member.Output, member.Member, member.Platform, member.Size, false, e.CodeGateUnmet},
		{"nil context", receipt, nil, member.Output, member.Member, member.Platform, member.Size, false, e.CodeUsage},
		{"canceled", receipt, canceled, member.Output, member.Member, member.Platform, member.Size, false, e.CodeTimeout},
		{"nil destination", receipt, t.Context(), member.Output, member.Member, member.Platform, member.Size, true, e.CodeUsage},
		{"zero limit", receipt, t.Context(), member.Output, member.Member, member.Platform, 0, false, e.CodeUsage},
		{"over byte budget", receipt, t.Context(), member.Output, member.Member, member.Platform, member.Size - 1, false, e.CodeEngineCapability},
		{"unsupported platform", receipt, t.Context(), member.Output, member.Member, "darwin/arm64", member.Size, false, e.CodeEngineCapability},
		{"wrong platform", receipt, t.Context(), member.Output, member.Member, "linux/arm64", member.Size, false, e.CodeVerifyFailed},
		{"unknown output", receipt, t.Context(), "absent", member.Member, member.Platform, member.Size, false, e.CodeVerifyFailed},
		{"wrong member", receipt, t.Context(), member.Output, "absent", member.Platform, member.Size, false, e.CodeVerifyFailed},
		{"unsafe member", receipt, t.Context(), member.Output, "bad\x1b", member.Platform, member.Size, false, e.CodeUsage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buffer bytes.Buffer
			var writer io.Writer = &buffer
			if tc.nilWriter {
				writer = nil
			}
			got, err := tc.receipt.WriteExecutableMember(tc.ctx, tc.output, tc.component, tc.platform, tc.limit, writer)
			assertCode(t, err, tc.code)
			if got != (ExecutableMemberDescription{}) || buffer.Len() != 0 {
				t.Fatal("failed admission wrote bytes or returned description")
			}
		})
	}
	var output bytes.Buffer
	got, err := receipt.WriteExecutableMember(t.Context(), member.Output, member.Member, member.Platform, member.Size, &output)
	if err != nil || got != member || int64(output.Len()) != member.Size || pipeline.FileDigest(output.Bytes()) != member.PayloadDigest {
		t.Fatalf("member=%+v bytes=%d err=%v", got, output.Len(), err)
	}
	// A detached description does not substitute for a package-issued receipt.
	detached := got
	detached.PayloadDigest = "sha256:" + strings.Repeat("0", 64)
	_, err = (&ArtifactCheckReceipt{}).WriteExecutableMember(t.Context(), detached.Output, detached.Member, detached.Platform, detached.Size, io.Discard)
	assertCode(t, err, e.CodeGateUnmet)
}

func TestArtifactExecutableMemberChecksRetainedCanonicalSelection(t *testing.T) {
	receipt, member := executableReceiptFixture(t)
	for _, tc := range []struct {
		name string
		edit func(*ArtifactCheckDescription)
		code e.Code
	}{
		{"non executable", func(d *ArtifactCheckDescription) {
			for i := range d.Outputs {
				if d.Outputs[i].Name == member.Output {
					d.Outputs[i].Kind = OutputOCI
				}
			}
		}, e.CodeEngineCapability},
		{"substituted subject", func(d *ArtifactCheckDescription) {
			for i := range d.Outputs {
				if d.Outputs[i].Name == member.Output {
					d.Outputs[i].Subject = "sha256:" + strings.Repeat("a", 64)
				}
			}
		}, e.CodeVerifyFailed},
		{"wrong member build subject", func(d *ArtifactCheckDescription) {
			for i := range d.Outputs {
				if d.Outputs[i].Name == member.Output {
					d.Outputs[i].BuildSubjects[0].Digest = "sha256:" + strings.Repeat("a", 64)
				}
			}
		}, e.CodeVerifyFailed},
		{"missing member build subject", func(d *ArtifactCheckDescription) {
			for i := range d.Outputs {
				if d.Outputs[i].Name == member.Output {
					d.Outputs[i].BuildSubjects = nil
				}
			}
		}, e.CodeVerifyFailed},
		{"duplicate output", func(d *ArtifactCheckDescription) {
			for _, o := range d.Outputs {
				if o.Name == member.Output {
					d.Outputs = append(d.Outputs, o)
					break
				}
			}
		}, e.CodeVerifyFailed},
		{"wrong root", func(d *ArtifactCheckDescription) {
			d.Inputs.ReleaseSubject = "registry.example.test/other@sha256:" + strings.Repeat("a", 64)
		}, e.CodeVerifyFailed},
		{"wrong source invocation", func(d *ArtifactCheckDescription) {
			d.Origin.InvocationID = "https://github.com/example/nvkr/actions/runs/99/attempts/1"
		}, e.CodeVerifyFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			description, err := receipt.Description()
			if err != nil {
				t.Fatal(err)
			}
			tc.edit(&description)
			raw, err := canonical.Marshal(description)
			if err != nil {
				t.Fatal(err)
			}
			// Private corruption test of an actually authenticated fixture; not an issuer.
			copyReceipt := *receipt
			copyReceipt.raw = string(raw)
			var buffer bytes.Buffer
			got, err := copyReceipt.WriteExecutableMember(t.Context(), member.Output, member.Member, member.Platform, member.Size, &buffer)
			assertCode(t, err, tc.code)
			if got != (ExecutableMemberDescription{}) || buffer.Len() != 0 {
				t.Fatal("substituted selection reached destination")
			}
		})
	}
}

type executableWriter func([]byte) (int, error)

func (w executableWriter) Write(raw []byte) (int, error) { return w(raw) }

func TestArtifactExecutableMemberRejectsPartialAndRevokedStreams(t *testing.T) {
	receipt, member := executableReceiptFixture(t)
	sentinel := errors.New("bounded destination refused bytes")
	got, err := receipt.WriteExecutableMember(t.Context(), member.Output, member.Member, member.Platform, member.Size, executableWriter(func(raw []byte) (int, error) { return 0, sentinel }))
	assertCode(t, err, e.CodeVerifyFailed)
	if !errors.Is(err, sentinel) || got != (ExecutableMemberDescription{}) {
		t.Fatal("writer failure lost cause or returned successful description")
	}
	entered, release := make(chan struct{}), make(chan struct{})
	streamed := make(chan error, 1)
	closed := make(chan error, 1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		_, err := receipt.WriteExecutableMember(ctx, member.Output, member.Member, member.Platform, member.Size, executableWriter(func(raw []byte) (int, error) { close(entered); <-release; return len(raw), nil }))
		streamed <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not enter destination")
	}
	copyReceipt := *receipt
	go func() { closed <- copyReceipt.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned before joined bounded writer: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	close(release)
	select {
	case err := <-streamed:
		assertCode(t, err, e.CodeTimeout)
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not join")
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not join")
	}
	_, err = receipt.WriteExecutableMember(t.Context(), member.Output, member.Member, member.Platform, member.Size, io.Discard)
	assertCode(t, err, e.CodeGateUnmet)
}

func TestArtifactExecutableMemberRequiresLiveBorrowedSource(t *testing.T) {
	for _, phase := range []string{"before", "during"} {
		t.Run(phase, func(t *testing.T) {
			receipt, member := executableReceiptFixture(t)
			source, ok := receipt.origin.(*qualificationFixtureSource)
			if !ok {
				t.Fatal("fixture did not retain its borrowed source")
			}
			if phase == "before" {
				source.closed = true
			}
			writes := 0
			got, err := receipt.WriteExecutableMember(t.Context(), member.Output, member.Member, member.Platform, member.Size, executableWriter(func(raw []byte) (int, error) { writes++; source.closed = true; return len(raw), nil }))
			assertCode(t, err, e.CodeProvenanceMissing)
			if got != (ExecutableMemberDescription{}) || phase == "before" && writes != 0 {
				t.Fatal("revoked source produced successful member metadata or wrote before admission")
			}
		})
	}
}
