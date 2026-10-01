package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/ownedcmd"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/target"
)

// PrebuiltTarget is a prebuilt component's acquisition, resolved against its version
// pin for `catalog pin-artifacts`: URL/Image are version-rendered (a binary URL still
// carries the {{.Arch}} token; an image ref does not), and Tag records the version the
// resolved digests will correspond to.
type PrebuiltTarget struct {
	PinnedBy  string
	Chart     *HelmChart
	Platforms []string
	Name      string
	Tag       string
	URL       string
	Image     string
}

func prebuiltTargetsFrom(reg *registryDoc, lock *PinLock) ([]PrebuiltTarget, error) {
	names := make([]string, 0, len(reg.Components))
	for n := range reg.Components {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []PrebuiltTarget
	for _, name := range names {
		raw := reg.Components[name]
		if raw.Prebuilt == nil {
			continue
		}
		_, pin, err := memberVersionPin(name, raw, reg.Sources, lock)
		if err != nil {
			return nil, err
		}
		owner := raw.PinnedBy
		if owner == "" {
			owner = reg.Sources[raw.Source].PinnedBy
		}
		selected, err := prebuiltTargetFor(name, raw, pin)
		if err != nil {
			return nil, err
		}
		selected.PinnedBy = owner
		out = append(out, selected)
	}
	return out, nil
}

func prebuiltTargetFor(name string, raw rawComponent, pin Pin) (PrebuiltTarget, error) {
	url, err := renderRef("prebuilt.url", raw.Prebuilt.URL, pin)
	if err != nil {
		return PrebuiltTarget{}, e.Wrap(e.CodeInvalidCatalog, err, "prebuilt component %q", name)
	}
	image, err := renderRef("prebuilt.image", raw.Prebuilt.Image, pin)
	if err != nil {
		return PrebuiltTarget{}, e.Wrap(e.CodeInvalidCatalog, err, "prebuilt component %q", name)
	}
	return PrebuiltTarget{
		Name:      name,
		Tag:       pin.Tag,
		Platforms: append([]string(nil), raw.Platforms...),
		URL:       url,
		Image:     image,
		Chart:     raw.Prebuilt.Chart,
	}, nil
}

func resolvePrebuiltTarget(ctx context.Context, selected PrebuiltTarget, runtime string) (ArtifactPin, error) {
	pin := ArtifactPin{Tag: selected.Tag, Digests: map[string]string{}}
	arches := make([]string, 0, len(selected.Platforms))
	for _, platform := range selected.Platforms {
		arches = append(arches, target.Arch(platform))
	}
	if selected.Chart != nil {
		chart := *selected.Chart
		pin.Chart = &chart
		url, digest, err := resolveHelmChart(ctx, *selected.Chart, NormalizeVersion(selected.Tag))
		if err != nil {
			return pin, err
		}
		pin.URL = url
		for _, arch := range arches {
			pin.Digests[arch] = digest
		}
		return pin, nil
	}
	if selected.Image != "" {
		digests, err := resolveImageDigests(ctx, runtime, selected.Image, arches)
		pin.Digests = digests
		return pin, err
	}
	for _, arch := range arches {
		digest, err := resolveBinaryDigest(ctx, strings.ReplaceAll(selected.URL, "{{.Arch}}", arch))
		if err != nil {
			return pin, err
		}
		pin.Digests[arch] = digest
	}
	return pin, nil
}

// hex64 matches a bare 64-hex sha256 body (e.g. a dl.k8s.io .sha256 sidecar).
var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func artifactPinEqual(a, b ArtifactPin) bool {
	if !reflect.DeepEqual(a.Chart, b.Chart) {
		return false
	}
	if a.DerivedFrom != b.DerivedFrom || a.URL != b.URL || a.Tag != b.Tag || len(a.Digests) != len(b.Digests) {
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
