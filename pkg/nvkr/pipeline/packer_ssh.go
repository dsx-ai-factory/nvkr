// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/internal/heldfile"
)

func prepareNativePackerSSH(ctx context.Context, toolchain *PackerToolchain, root string, environment []string) (string, string, error) {
	var executable packerExecutable
	for _, tool := range toolchain.hostTools {
		if tool.name == "ssh-keygen" {
			executable = tool.file
		}
	}
	if executable == nil {
		return "", "", e.New(e.CodeEngineCapability, "native Packer SSH mapping requires declared pinned ssh-keygen")
	}
	directory := filepath.Join(root, "ssh")
	if err := os.Mkdir(directory, 0o700); err != nil {
		return "", "", e.Wrap(e.CodeBuildFailed, err, "creating private Packer SSH directory")
	}
	private := filepath.Join(directory, "id_ed25519")
	options := heldfile.RunOptions{Env: environment, Dir: root, Stdout: io.Discard, Stderr: io.Discard}
	if err := toolchain.execution.run(ctx, executable, nil, []string{"-q", "-t", "ed25519", "-N", "", "-f", private}, options); err != nil {
		return "", "", e.Wrap(e.CodeEngineCapability, err, "generating private Packer SSH key")
	}
	var fingerprints [2]string
	for index, name := range []string{"id_ed25519", "id_ed25519.pub"} {
		file, info, err := openRegularBeneath(directory, name)
		if err != nil {
			return "", "", e.Wrap(e.CodeVerifyFailed, err, "opening generated Packer SSH key")
		}
		closeErr := file.Close()
		if closeErr != nil {
			return "", "", e.Wrap(e.CodeVerifyFailed, closeErr, "closing generated Packer SSH key")
		}
		if info.Size() <= 0 || info.Size() > 16<<10 || (index == 0 && info.Mode().Perm() != 0o600) {
			return "", "", e.New(e.CodeVerifyFailed, "generated Packer SSH key has invalid size or permissions")
		}
		capture := newPackerValidationOutput(io.Discard)
		options.Stdout = capture
		if err := toolchain.execution.run(ctx, executable, nil, []string{"-l", "-E", "sha256", "-f", filepath.Join(directory, name)}, options); err != nil {
			return "", "", e.Wrap(e.CodeEngineCapability, err, "verifying private Packer SSH key pair")
		}
		if capture.overflow {
			return "", "", e.New(e.CodeEngineCapability, "ssh-keygen fingerprint output exceeds its bound")
		}
		fingerprint, err := packerSSHKeyFingerprint(capture.buffer.Bytes())
		if err != nil {
			return "", "", err
		}
		fingerprints[index] = fingerprint
	}
	if fingerprints[0] != fingerprints[1] {
		return "", "", e.New(e.CodeEngineCapability, "attempt-local SSH public and private keys do not match")
	}
	return private, private + ".pub", nil
}

// ssh-keygen owns key parsing and validity. NVKR compares its opaque reported
// fingerprints; it does not parse or implement the SSH key format.
func packerSSHKeyFingerprint(raw []byte) (string, error) {
	fields := strings.Fields(string(raw))
	if len(fields) < 2 || !strings.HasPrefix(fields[1], "SHA256:") {
		return "", e.New(e.CodeEngineCapability, "pinned ssh-keygen returned no fingerprint")
	}
	return fields[1], nil
}
