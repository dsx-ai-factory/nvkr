package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/ownedcmd"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// hex64 matches a bare 64-hex sha256 body (e.g. a dl.k8s.io .sha256 sidecar).
var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func artifactPinEqual(a, b ArtifactPin) bool {
	if a.Tag != b.Tag || len(a.Digests) != len(b.Digests) {
		return false
	}
	for k, v := range b.Digests {
		if a.Digests[k] != v {
			return false
		}
	}
	return true
}

// resolveBinaryDigest resolves an artifact URL to its pinned sha256, preferring the
// published <url>.sha256 sidecar (authoritative, tiny) and falling back to downloading
// and hashing the artifact itself.
func resolveBinaryDigest(ctx context.Context, url string) (string, error) {
	if body, err := httpGetBody(ctx, url+".sha256", 64<<10); err == nil {
		if fields := strings.Fields(strings.TrimSpace(string(body))); len(fields) > 0 && hex64.MatchString(fields[0]) {
			return "sha256:" + fields[0], nil
		}
	} else if ctx.Err() != nil {
		return "", e.Wrap(e.CodeTimeout, ctx.Err(), "resolving binary digest canceled")
	}
	return httpSHA256(ctx, url, 2<<30)
}

// resolveImageDigests reads a multi-arch image's per-arch manifest digests via
// `docker manifest inspect`, returning arch -> sha256:<digest> for each requested arch
// (fail closed if an arch is absent from the manifest list).
func resolveImageDigests(ctx context.Context, docker, ref string, arches []string) (map[string]string, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "image digest resolution requires a context")
	}
	out, err := ownedcmd.Output(ctx, docker, "manifest", "inspect", ref)
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "docker manifest inspection canceled")
		}
		return nil, e.Wrap(e.CodeFetchFailed, err, "docker manifest inspect %s", ref)
	}
	var idx struct {
		Manifests []struct {
			Digest   string `json:"digest"`
			Platform struct {
				Architecture string `json:"architecture"`
				OS           string `json:"os"`
			} `json:"platform"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(out, &idx); err != nil {
		return nil, e.Wrap(e.CodeFetchFailed, err, "parsing manifest list of %s", ref)
	}
	byArch := map[string]string{}
	for _, m := range idx.Manifests {
		if m.Platform.OS == "linux" {
			byArch[m.Platform.Architecture] = m.Digest
		}
	}
	res := map[string]string{}
	for _, a := range arches {
		d, ok := byArch[a]
		if !ok {
			return nil, e.New(e.CodeFetchFailed, "image %s has no linux/%s manifest", ref, a)
		}
		res[a] = d
	}
	return res, nil
}

// httpGetBody fetches a URL and returns its body, erroring on a non-200 status.
func httpGetBody(ctx context.Context, url string, maxBytes int64) ([]byte, error) {
	resp, err := catalogHTTPResponse(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.ContentLength > maxBytes {
		return nil, e.New(e.CodeFetchFailed, "GET %s exceeds %d bytes", url, maxBytes)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "GET %s canceled", url)
		}
		return nil, e.Wrap(e.CodeFetchFailed, err, "reading GET %s", url)
	}
	if int64(len(body)) > maxBytes {
		return nil, e.New(e.CodeFetchFailed, "GET %s exceeds %d bytes", url, maxBytes)
	}
	return body, nil
}

func httpSHA256(ctx context.Context, url string, maxBytes int64) (string, error) {
	resp, err := catalogHTTPResponse(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.ContentLength > maxBytes {
		return "", e.New(e.CodeFetchFailed, "GET %s exceeds %d bytes", url, maxBytes)
	}
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return "", e.Wrap(e.CodeTimeout, ctx.Err(), "GET %s canceled", url)
		}
		return "", e.Wrap(e.CodeFetchFailed, err, "hashing GET %s", url)
	}
	if written > maxBytes {
		return "", e.New(e.CodeFetchFailed, "GET %s exceeds %d bytes", url, maxBytes)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func catalogHTTPResponse(ctx context.Context, url string) (*http.Response, error) {
	if ctx == nil {
		return nil, e.New(e.CodeUsage, "catalog HTTP resolution requires a context")
	}
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "catalog HTTP resolution canceled")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, e.Wrap(e.CodeFetchFailed, err, "constructing GET %s", url)
	}
	response, err := newPinHTTPClient().Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "GET %s canceled", url)
		}
		return nil, e.Wrap(e.CodeUnavailable, err, "GET %s", url)
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, e.New(e.CodeUnavailable, "GET %s returned HTTP %s", url, response.Status)
	}
	return response, nil
}

func newPinHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	transport.TLSHandshakeTimeout = 15 * time.Second
	transport.IdleConnTimeout = 30 * time.Second
	transport.ExpectContinueTimeout = time.Second
	return &http.Client{Transport: transport, Timeout: 5 * time.Minute}
}
