// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package imagepreflight

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

const maxMetadataBytes = 4 << 20
const acceptManifests = "application/vnd.oci.image.manifest.v1+json,application/vnd.oci.image.index.v1+json,application/vnd.docker.distribution.manifest.v2+json,application/vnd.docker.distribution.manifest.list.v2+json"

type refusal string

func (err refusal) Error() string { return string(err) }

// metadataTransport bounds every request, including challenges and token
// exchanges. Only the exact selected manifests and subsequently authenticated
// config can be requested. Redirects cannot broaden credentials or pull scope.
type metadataTransport struct {
	base                 http.RoundTripper
	registry, repository string
	private              bool
	paths                map[string]bool
	configRedirects      map[string]bool
}

func (t *metadataTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	if request.URL.Scheme != "https" || request.URL.User != nil || (request.Method != http.MethodGet && request.Method != http.MethodHead) {
		return nil, refusal("unsupported registry request")
	}
	sameHost := request.URL.Host == t.registry
	scope := "repository:" + t.repository + ":pull"
	scopes := request.URL.Query()["scope"]
	token := len(scopes) == 1 && scopes[0] == scope
	configRedirect := request.Method == http.MethodGet && request.Header.Get("Authorization") == "" && t.configRedirects[request.URL.String()]
	if !configRedirect && ((!sameHost && (t.private || request.Header.Get("Authorization") != "")) ||
		(!token && (!sameHost || !t.paths[request.URL.Path])) || (len(scopes) != 0 && !token)) {
		return nil, refusal("registry request exceeds admitted route")
	}
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(request.Context(), requestTimeout)
		response, err := t.base.RoundTrip(request.Clone(ctx))
		if err == nil {
			limit := int64(maxMetadataBytes)
			if token {
				limit = 64 << 10
			}
			var raw []byte
			if response.ContentLength > limit && request.Method != http.MethodHead {
				err = refusal("registry response exceeds metadata limit")
			} else {
				raw, err = io.ReadAll(io.LimitReader(response.Body, limit+1))
				if int64(len(raw)) > limit {
					err = refusal("registry response exceeds metadata limit")
				}
			}
			closeErr := response.Body.Close()
			if err == nil {
				err = closeErr
			}
			if err == nil {
				response.Body = io.NopCloser(bytes.NewReader(raw))
			}
			// The first registry challenge is protocol negotiation. Once it is
			// complete, auth/not-found failures are terminal, even if a server
			// appends a new challenge. Do not let the auth transport refresh or
			// expand scopes in response, or log a foreign challenge/body.
			if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusNotFound ||
				(response.StatusCode == http.StatusUnauthorized && request.URL.Path != "/v2/") {
				err = statusRefusal(response.StatusCode)
			}
			if response.StatusCode == http.StatusUnauthorized && request.URL.Path == "/v2/" {
				challenge := strings.ToLower(response.Header.Get("WWW-Authenticate"))
				if !strings.HasPrefix(challenge, "bearer ") && !strings.HasPrefix(challenge, "basic ") {
					err = refusal("unsupported registry challenge")
				}
			}
		}
		cancel()
		if err != nil && response != nil {
			response = nil
		}
		transient := err != nil && !errors.As(err, new(refusal))
		if err == nil {
			transient = response.StatusCode == 429 || response.StatusCode == 500 || response.StatusCode == 502 || response.StatusCode == 503 || response.StatusCode == 504
		}
		if !transient || attempt == 1 || request.Context().Err() != nil {
			return response, err
		}
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		timer := time.NewTimer(150 * time.Millisecond)
		select {
		case <-request.Context().Done():
			timer.Stop()
			return nil, request.Context().Err()
		case <-timer.C:
		}
	}
	return nil, refusal("registry retry bound exhausted")
}

func probe(ctx context.Context, subject Subject, credential Credential, base http.RoundTripper) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", probeError(ctx, err)
	}
	pin, _ := name.NewDigest(subject.Image) // complete selection was admitted before workers started
	path := "/v2/" + pin.RepositoryStr() + "/manifests/" + pin.DigestStr()
	bounded := &metadataTransport{base: base, registry: pin.RegistryStr(), repository: pin.RepositoryStr(), private: subject.Route != Anonymous, paths: map[string]bool{"/v2/": true, path: true}}
	auth := authn.Anonymous
	if bounded.private {
		auth = &authn.Basic{Username: credential.Username, Password: credential.Token}
	}
	rt, err := transport.NewWithContext(ctx, pin.Registry, auth, bounded, []string{pin.Scope(transport.PullScope)})
	if err != nil {
		return "", probeError(ctx, err)
	}
	client := &http.Client{Transport: rt, CheckRedirect: func(*http.Request, []*http.Request) error { return refusal("registry redirects are not admitted") }}
	raw, media, err := readManifest(ctx, client, bounded, pin.DigestStr())
	if err != nil {
		return "", probeError(ctx, err)
	}
	selected := pin.DigestStr()
	wanted, _ := v1.ParsePlatform(subject.Platform)
	if media.IsIndex() {
		var index v1.IndexManifest
		if json.Unmarshal(raw, &index) != nil || index.SchemaVersion != 2 ||
			(index.MediaType != media && (media != types.OCIImageIndex || index.MediaType != "")) {
			return "", probeError(ctx, refusal("malformed image index"))
		}
		var matches []v1.Descriptor
		for _, child := range index.Manifests {
			if child.Platform != nil && platformMatches(*child.Platform, *wanted) {
				matches = append(matches, child)
			}
		}
		if len(matches) != 1 {
			return "", e.New(e.CodeEngineCapability, "image preflight requires one exact platform manifest")
		}
		child := matches[0]
		if !child.MediaType.IsImage() || !canonical.IsDigest(child.Digest.String()) || child.Size <= 0 || child.Size > maxMetadataBytes || len(child.URLs) != 0 {
			return "", probeError(ctx, refusal("malformed platform manifest descriptor"))
		}
		selected = child.Digest.String()
		bounded.paths["/v2/"+pin.RepositoryStr()+"/manifests/"+selected] = true
		raw, media, err = readManifest(ctx, client, bounded, selected)
		if err != nil {
			return "", probeError(ctx, err)
		}
		if int64(len(raw)) != child.Size || media != child.MediaType {
			return "", probeError(ctx, refusal("platform manifest descriptor mismatch"))
		}
	}
	var manifest v1.Manifest
	if !media.IsImage() || json.Unmarshal(raw, &manifest) != nil || manifest.SchemaVersion != 2 || (manifest.MediaType != "" && manifest.MediaType != media) || manifest.Layers == nil {
		return "", probeError(ctx, refusal("malformed image manifest"))
	}
	config := manifest.Config
	for _, layer := range manifest.Layers {
		if !canonical.IsDigest(layer.Digest.String()) || layer.Size < 0 || !layer.MediaType.IsLayer() {
			return "", probeError(ctx, refusal("malformed layer descriptor"))
		}
	}
	if !canonical.IsDigest(config.Digest.String()) || config.Size <= 0 || config.Size > maxMetadataBytes || len(config.URLs) != 0 ||
		(config.MediaType != types.OCIConfigJSON && config.MediaType != types.DockerConfigJSON) {
		return "", probeError(ctx, refusal("malformed image config descriptor"))
	}
	configPath := "/v2/" + pin.RepositoryStr() + "/blobs/" + config.Digest.String()
	bounded.paths[configPath] = true
	// Registry config blobs may use signed CDN redirects. Only this config
	// request may follow them, without reader credentials; its exact digest,
	// descriptor size and platform are still checked below. Layer paths never
	// enter the allowlist. Token exchanges retain their stricter route guard.
	bounded.configRedirects = map[string]bool{}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		ip := net.ParseIP(request.URL.Hostname())
		if len(via) == 0 || len(via) > 3 || via[0].URL.Path != configPath || request.URL.Scheme != "https" || request.URL.User != nil ||
			(ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified())) {
			return refusal("config redirect exceeds admitted route")
		}
		request.Header.Del("Authorization")
		bounded.configRedirects[request.URL.String()] = true
		return nil
	}
	response, err := metadataRequest(ctx, client, bounded.registry, http.MethodGet, configPath)
	if err != nil {
		return "", probeError(ctx, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", probeError(ctx, statusRefusal(response.StatusCode))
	}
	raw, err = io.ReadAll(response.Body)
	if err != nil || int64(len(raw)) != config.Size || blobDigest(raw) != config.Digest.String() {
		return "", probeError(ctx, refusal("image config digest or size mismatch"))
	}
	var file v1.ConfigFile
	if json.Unmarshal(raw, &file) != nil {
		return "", probeError(ctx, refusal("malformed image config"))
	}
	if !platformMatches(v1.Platform{OS: file.OS, Architecture: file.Architecture, Variant: file.Variant}, *wanted) {
		return "", e.New(e.CodeEngineCapability, "image preflight config does not match the requested platform")
	}
	return selected, nil
}

func platformMatches(got, want v1.Platform) bool {
	return got.OS == want.OS && got.Architecture == want.Architecture && (want.Variant == "" || got.Variant == want.Variant)
}

func readManifest(ctx context.Context, client *http.Client, bounded *metadataTransport, digest string) ([]byte, types.MediaType, error) {
	path := "/v2/" + bounded.repository + "/manifests/" + digest
	response, err := metadataRequest(ctx, client, bounded.registry, http.MethodHead, path)
	if err != nil {
		return nil, "", err
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusMethodNotAllowed && response.StatusCode != http.StatusNotImplemented {
		return nil, "", statusRefusal(response.StatusCode)
	}
	if response.StatusCode == http.StatusOK && response.Header.Get("Docker-Content-Digest") != "" && response.Header.Get("Docker-Content-Digest") != digest {
		return nil, "", refusal("manifest HEAD digest mismatch")
	}
	// An exact GET is also needed after a successful HEAD: HEAD alone proves
	// neither an index's platform membership nor an image config's platform.
	response, err = metadataRequest(ctx, client, bounded.registry, http.MethodGet, path)
	if err != nil {
		return nil, "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, "", statusRefusal(response.StatusCode)
	}
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, "", err
	}
	if blobDigest(raw) != digest || (response.Header.Get("Docker-Content-Digest") != "" && response.Header.Get("Docker-Content-Digest") != digest) {
		return nil, "", refusal("manifest digest mismatch")
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	media := types.MediaType(mediaType)
	if err != nil || (!media.IsImage() && !media.IsIndex()) {
		return nil, "", refusal("unsupported manifest media type")
	}
	return raw, media, nil
}

func metadataRequest(ctx context.Context, client *http.Client, registry, method, path string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, "https://"+registry+path, nil)
	if err != nil {
		return nil, refusal("invalid metadata request")
	}
	request.Header.Set("Accept", acceptManifests)
	return client.Do(request)
}

func blobDigest(raw []byte) string {
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func statusRefusal(status int) error {
	switch status {
	case http.StatusUnauthorized:
		return refusal("reader unauthorized")
	case http.StatusForbidden:
		return refusal("reader forbidden")
	case http.StatusNotFound:
		return refusal("exact registry object not found")
	default:
		return refusal("registry metadata request failed")
	}
}

func probeError(ctx context.Context, err error) error {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return e.New(e.CodeTimeout, "image preflight request canceled or timed out")
	}
	var safe refusal
	if errors.As(err, &safe) {
		return e.New(e.CodeFetchFailed, "image preflight: %s", safe)
	}
	var registry *transport.Error
	if errors.As(err, &registry) {
		return e.New(e.CodeFetchFailed, "image preflight: %s", statusRefusal(registry.StatusCode))
	}
	// Do not wrap external errors: URLs, challenges and bodies may carry secrets.
	return e.New(e.CodeFetchFailed, "image preflight registry exchange failed")
}
