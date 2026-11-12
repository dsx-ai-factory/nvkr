// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package sign

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

// OCIKeyTrust explicitly maps one independently selected public key to a
// workload identity and authority. It does not assert an OIDC certificate,
// transparency inclusion, or exact-byte reproduction. The keyless path never
// falls back to this policy when certificate verification fails.
type OCIKeyTrust struct {
	PublicKey []byte
	Identity  string
}

func normalizeOCIKeyTrust(policy *OCIKeyTrust, identity string) (*OCIKeyTrust, error) {
	if policy == nil {
		return nil, nil
	}
	if policy.Identity == "" || identity != "^"+regexp.QuoteMeta(policy.Identity)+"$" {
		return nil, e.New(e.CodeTrustPolicyMissing, "keyed OCI trust requires its exact independently mapped identity")
	}
	public, err := NormalizeLocalPublicKey(policy.PublicKey)
	if err != nil {
		return nil, err
	}
	return &OCIKeyTrust{PublicKey: public, Identity: policy.Identity}, nil
}

// keySigningArguments freezes explicitly selected Secret bytes in the private
// operation workspace. No provider token, ambient key, or OIDC credential is
// offered to cosign in this mode. All callers suppress parser diagnostics.
func (s *CosignOCISigner) keySigningArguments(ctx context.Context, args []string, workspace string) ([]string, error) {
	if s.keyTrust == nil {
		return args, nil
	}
	if s.privateKeyPath == "" {
		return nil, e.New(e.CodeTrustPolicyMissing, "keyed OCI signing requires its explicit private key mount")
	}
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	file, err := os.Open(s.privateKeyPath)
	if err != nil {
		return nil, e.Wrap(e.CodeTrustPolicyMissing, err, "opening explicit OCI signing key")
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, e.Wrap(e.CodeTrustPolicyMissing, err, "checking explicit OCI signing key")
	}
	if !info.Mode().IsRegular() || info.Size() > maxLocalKeyBytes {
		_ = file.Close()
		return nil, e.New(e.CodeTrustPolicyMissing, "OCI signing key is not a bounded regular file")
	}
	private, err := io.ReadAll(io.LimitReader(file, maxLocalKeyBytes+1))
	closeErr := file.Close()
	defer clear(private)
	if err != nil || closeErr != nil || len(private) == 0 || len(private) > maxLocalKeyBytes {
		return nil, e.New(e.CodeTrustPolicyMissing, "OCI signing key could not be captured within its bound")
	}
	path := filepath.Join(workspace, "key-"+args[0]+".pem")
	if err := writePrivateFile(ctx, path, private); err != nil {
		return nil, err
	}
	// Keep the single subject positional argument last.
	result := append([]string(nil), args[:len(args)-1]...)
	result = append(result, "--key", path, "--use-signing-config=false", "--tlog-upload=false")
	return append(result, args[len(args)-1]), nil
}

func keyCosignEnvironment(base []string) []string {
	result := make([]string, 0, len(base)+1)
	for _, entry := range base {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		switch upper {
		case "PATH", "HOME", "DOCKER_CONFIG", "TMPDIR", "TMP", "TEMP", "SSL_CERT_FILE", "SSL_CERT_DIR", "LANG", "LC_ALL":
			result = append(result, entry)
		}
	}
	return append(result, "COSIGN_PASSWORD=")
}

func keyedBundleArguments(ctx context.Context, args []string, workspace string, policy *OCIKeyTrust) ([]string, error) {
	if policy == nil {
		return args, nil
	}
	path := filepath.Join(workspace, "public-key.pem")
	// One operation may verify several bundles under the same immutable key.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := writePrivateFile(ctx, path, policy.PublicKey); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, e.Wrap(e.CodeTrustPolicyMissing, err, "checking OCI verification key")
	}
	if err := assertPrivateFileUnchanged(path, policy.PublicKey); err != nil {
		return nil, err
	}
	// Explicit key trust has no public transparency or certificate authority.
	// Supply the empty root so offline verification cannot bootstrap public TUF.
	root := []byte(`{"mediaType":"application/vnd.dev.sigstore.trustedroot+json;version=0.1","tlogs":[],"certificateAuthorities":[],"ctlogs":[],"timestampAuthorities":[]}`)
	rootPath := filepath.Join(workspace, "key-trusted-root.json")
	if _, err := os.Stat(rootPath); os.IsNotExist(err) {
		if err := writePrivateFile(ctx, rootPath, root); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, e.Wrap(e.CodeTrustPolicyMissing, err, "checking explicit key trust root")
	}
	if err := assertPrivateFileUnchanged(rootPath, root); err != nil {
		return nil, err
	}
	var result []string
	for index := 0; index < len(args); index++ {
		if args[index] == "--certificate-identity-regexp" || args[index] == "--certificate-oidc-issuer" {
			index++
			continue
		}
		result = append(result, args[index])
	}
	return append(result, "--key", path, "--trusted-root", rootPath, "--insecure-ignore-tlog", "--offline"), nil
}
