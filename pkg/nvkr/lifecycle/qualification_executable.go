// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package lifecycle

import (
	"context"
	"errors"
	"io"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/safetext"
)

// ExecutableMemberDescription describes bytes streamed from one exact member of
// the receipt's authenticated publication. It cannot restore payload, source,
// execution, smoke, qualification or release authority.
type ExecutableMemberDescription struct {
	Output        string `json:"output"`
	Member        string `json:"member"`
	Platform      string `json:"platform"`
	PayloadDigest string `json:"payloadDigest"`
	Size          int64  `json:"size"`
}

// WriteExecutableMember streams an exact authenticated executable output member.
// The selected platform and maximum size are admitted before any destination
// write; delivery checks the complete retained archive before success. This
// grants payload access only, without smoke or qualification authorization.
//
// The caller owns destination and must bound blocking writes. An error may leave
// partial bytes, which must be discarded. Do not Close this receipt inside Write:
// Close waits for the stream. Callers borrowing source-bound tools must register
// their complete copy, seal, use and cleanup lifetime with WithDiagnosticOperation;
// this method's registered operation ends when streaming returns. Separately
// borrowed source revocation is checked at registration and completion, rather
// than asynchronously canceling a blocked destination.
func (receipt *ArtifactCheckReceipt) WriteExecutableMember(ctx context.Context, outputName, memberName, platform string, maxBytes int64, destination io.Writer) (ExecutableMemberDescription, error) {
	if ctx == nil {
		return ExecutableMemberDescription{}, e.New(e.CodeUsage, "executable member streaming requires context")
	}
	if err := ctx.Err(); err != nil {
		return ExecutableMemberDescription{}, e.Wrap(e.CodeTimeout, err, "executable member streaming canceled")
	}
	if destination == nil || maxBytes <= 0 {
		return ExecutableMemberDescription{}, e.New(e.CodeUsage, "executable member streaming requires a bounded destination and positive byte limit")
	}
	for _, value := range []string{outputName, memberName} {
		if !safetext.Valid(value, 4096, false) || strings.TrimSpace(value) != value {
			return ExecutableMemberDescription{}, e.New(e.CodeUsage, "executable output and member names require explicit safe text")
		}
	}
	if platform != "linux/amd64" && platform != "linux/arm64" {
		return ExecutableMemberDescription{}, e.New(e.CodeEngineCapability, "executable member platform is unsupported")
	}
	operation, done, err := receipt.beginOperation(ctx)
	if err != nil {
		return ExecutableMemberDescription{}, err
	}
	defer done()
	description, err := receipt.Description()
	if err != nil {
		return ExecutableMemberDescription{}, err
	}
	var selected ArtifactCheckOutput
	matches := 0
	for _, output := range description.Outputs {
		if output.Name == outputName {
			selected = output
			matches++
		}
	}
	if matches != 1 {
		return ExecutableMemberDescription{}, e.New(e.CodeVerifyFailed, "executable member requires one exact authenticated named output")
	}
	if selected.Kind != OutputExecutable {
		return ExecutableMemberDescription{}, e.New(e.CodeEngineCapability, "selected output is not an executable bundle")
	}
	if selected.Platform != platform {
		return ExecutableMemberDescription{}, e.New(e.CodeVerifyFailed, "executable output does not match the selected platform")
	}
	verifiedDescription, err := receipt.lifetime.verified.Description()
	if err != nil {
		return ExecutableMemberDescription{}, err
	}
	if verifiedDescription.Subject != description.Inputs.ReleaseSubject {
		return ExecutableMemberDescription{}, e.New(e.CodeVerifyFailed, "executable selection does not match the verified publication root")
	}
	publication := receipt.lifetime.verified.publication
	publicationDescription, err := publication.Description(description.Inputs.ReleaseSubject)
	if err != nil {
		return ExecutableMemberDescription{}, err
	}
	publicationMatches := 0
	for _, output := range publicationDescription.Outputs {
		if output.Name == outputName {
			if output.Reference != selected.Reference || output.Digest != selected.Subject || output.BuildDigest != selected.BuildDigest || output.Kind != selected.Kind || output.OS+"/"+output.Arch != platform {
				return ExecutableMemberDescription{}, e.New(e.CodeVerifyFailed, "executable selection differs from the retained authenticated publication")
			}
			publicationMatches++
		}
	}
	if publicationMatches != 1 || publicationDescription.Metadata.InvocationID != description.Origin.InvocationID {
		return ExecutableMemberDescription{}, e.New(e.CodeVerifyFailed, "executable publication does not match its authenticated source binding")
	}
	bundle, err := publication.ExecutableBundle(outputName)
	if err != nil {
		return ExecutableMemberDescription{}, err
	}
	if bundle.OS+"/"+bundle.Arch != platform {
		return ExecutableMemberDescription{}, e.New(e.CodeVerifyFailed, "retained executable bundle does not match the selected platform")
	}
	member := ExecutableMemberDescription{Output: outputName, Member: memberName, Platform: platform}
	memberMatches := 0
	for _, entry := range bundle.Entries {
		if entry.Name == memberName {
			if entry.Mode&0o111 == 0 {
				return ExecutableMemberDescription{}, e.New(e.CodeEngineCapability, "selected member has no executable permission")
			}
			if entry.Size > maxBytes {
				return ExecutableMemberDescription{}, e.New(e.CodeEngineCapability, "selected executable member exceeds its byte limit")
			}
			member.PayloadDigest, member.Size = entry.SHA256, entry.Size
			memberMatches++
		}
	}
	if memberMatches != 1 {
		return ExecutableMemberDescription{}, e.New(e.CodeVerifyFailed, "executable bundle omits the exact selected member")
	}
	sourceMatches := 0
	for _, subject := range selected.BuildSubjects {
		if subject.Component == memberName {
			if subject.Digest != member.PayloadDigest {
				return ExecutableMemberDescription{}, e.New(e.CodeVerifyFailed, "executable member differs from its authenticated build subject")
			}
			sourceMatches++
		}
	}
	if sourceMatches != 1 {
		return ExecutableMemberDescription{}, e.New(e.CodeVerifyFailed, "executable member has no unique authenticated build subject")
	}
	if err := operation.Err(); err != nil {
		return ExecutableMemberDescription{}, e.Wrap(e.CodeTimeout, err, "executable member streaming canceled before write")
	}
	written, err := publication.WriteExecutableMember(operation, outputName, memberName, destination)
	if canceled := operation.Err(); canceled != nil {
		return ExecutableMemberDescription{}, e.Wrap(e.CodeTimeout, errors.Join(err, canceled), "executable member streaming canceled")
	}
	if err != nil {
		var coded *e.Error
		if errors.As(err, &coded) {
			return ExecutableMemberDescription{}, err
		}
		return ExecutableMemberDescription{}, e.Wrap(e.CodeVerifyFailed, err, "streaming authenticated executable member")
	}
	if written != member.Size {
		return ExecutableMemberDescription{}, e.New(e.CodeVerifyFailed, "streamed executable member has a different size")
	}
	if _, err := receipt.Description(); err != nil {
		return ExecutableMemberDescription{}, err
	}
	return member, nil
}
