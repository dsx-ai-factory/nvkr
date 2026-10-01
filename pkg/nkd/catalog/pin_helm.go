package catalog

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/internal/versionpolicy"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
	yamlv2 "go.yaml.in/yaml/v2"
	"sigs.k8s.io/yaml"
)

const (
	maxHelmIndexBytes   = 16 << 20
	maxHelmPackageBytes = 64 << 20
	maxHelmArchiveBytes = 256 << 20
	maxHelmChartBytes   = 1 << 20
	maxHelmFiles        = 10000
	helmConfigType      = types.MediaType("application/vnd.cncf.helm.config.v1+json")
	helmContentType     = types.MediaType("application/vnd.cncf.helm.chart.content.v1.tar+gzip")
)

var (
	helmChartName = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
)

// HelmChart selects an external chart repository. Its exact version comes from
// the pin; neither an index range nor a chart's dependency ranges are resolved.
type HelmChart struct {
	Name       string `json:"name"`
	Repository string `json:"repository"`
}

func validateHelmChart(chart HelmChart) error {
	if !helmChartName.MatchString(chart.Name) || len(chart.Name) > 253 {
		return e.New(e.CodeInvalidCatalog, "invalid Helm chart name %q", chart.Name)
	}
	u, err := url.Parse(chart.Repository)
	if err != nil || u == nil || (u.Scheme != "https" && u.Scheme != "oci") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return e.New(e.CodeInvalidCatalog, "Helm repository %q must be an https or oci URL without credentials, query, or fragment", chart.Repository)
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." || strings.Contains(segment, `\`) {
			return e.New(e.CodeInvalidCatalog, "Helm repository %q has an unsafe path", chart.Repository)
		}
	}
	if u.Scheme == "oci" {
		if _, err := name.NewRepository(strings.TrimPrefix(strings.TrimRight(chart.Repository, "/"), "oci://")+"/"+chart.Name, name.StrictValidation); err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "invalid OCI Helm repository")
		}
	}
	return nil
}

type helmFetch func(context.Context, string, int64) ([]byte, error)
type helmOCIReader func(context.Context, name.Reference) (v1.Image, error)

// resolveHelmChart pins the chart package bytes, not an OCI manifest or any image
// referenced by the chart. OCI acquisition is anonymous, matching the downstream
// URL acquisition contract; ambient Docker credentials are not authority here.
func resolveHelmChart(ctx context.Context, chart HelmChart, version string) (string, string, error) {
	return resolveHelmChartWith(ctx, chart, version, fetchHelmHTTPS, func(ctx context.Context, ref name.Reference) (v1.Image, error) {
		return remote.Image(ref, remote.WithContext(ctx), remote.WithAuth(authn.Anonymous), remote.WithTransport(helmTransport{remote.DefaultTransport}), remote.WithUserAgent("nkdctl/helm-pin"))
	})
}

func resolveHelmChartWith(ctx context.Context, chart HelmChart, version string, fetch helmFetch, oci helmOCIReader) (string, string, error) {
	if err := pinContextError(ctx); err != nil {
		return "", "", err
	}
	if err := validateHelmChart(chart); err != nil {
		return "", "", err
	}
	if !versionpolicy.Helm(version) {
		return "", "", e.New(e.CodeInvalidCatalog, "Helm chart %q requires an exact version, got %q", chart.Name, version)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var location, advertised string
	var archive []byte
	var err error
	if strings.HasPrefix(chart.Repository, "oci://") {
		location, advertised, archive, err = readHelmOCI(ctx, chart, version, oci)
	} else {
		location, advertised, err = selectHelmIndex(ctx, chart, version, fetch)
		if err == nil {
			archive, err = fetch(ctx, location, maxHelmPackageBytes)
		}
	}
	if err != nil {
		if ctx.Err() != nil {
			return "", "", e.Wrap(e.CodeTimeout, ctx.Err(), "Helm chart resolution canceled")
		}
		return "", "", err
	}
	if len(archive) > maxHelmPackageBytes {
		return "", "", e.New(e.CodeFetchFailed, "Helm chart package exceeds size bound")
	}
	hash := sha256.Sum256(archive)
	digest := "sha256:" + hex.EncodeToString(hash[:])
	if advertised != "" && digest != advertised {
		return "", "", e.New(e.CodeVerifyFailed, "Helm chart %q package digest %s differs from advertised %s", chart.Name, digest, advertised)
	}
	if err := validateHelmArchive(ctx, archive, chart.Name, version); err != nil {
		return "", "", err
	}
	return location, digest, nil
}

func selectHelmIndex(ctx context.Context, chart HelmChart, version string, fetch helmFetch) (string, string, error) {
	base, _ := url.Parse(strings.TrimRight(chart.Repository, "/") + "/") // validated by caller
	body, err := fetch(ctx, base.ResolveReference(&url.URL{Path: "index.yaml"}).String(), maxHelmIndexBytes)
	if err != nil {
		return "", "", err
	}
	var index struct {
		APIVersion string `json:"apiVersion"`
		Entries    map[string][]struct {
			Name    string   `json:"name"`
			Version string   `json:"version"`
			URLs    []string `json:"urls"`
			Digest  string   `json:"digest"`
		} `json:"entries"`
	}
	if len(body) > maxHelmIndexBytes {
		return "", "", e.New(e.CodeFetchFailed, "Helm index exceeds size bound")
	}
	if err := decodeHelmYAML(body, &index); err != nil {
		return "", "", e.Wrap(e.CodeInvalidCatalog, err, "invalid Helm index")
	}
	if index.APIVersion != "v1" {
		return "", "", e.New(e.CodeInvalidCatalog, "unsupported Helm index apiVersion %q", index.APIVersion)
	}
	var location, digest string
	matches := 0
	for _, entry := range index.Entries[chart.Name] {
		if entry.Version != version {
			continue
		}
		matches++
		if entry.Name != chart.Name || len(entry.URLs) != 1 {
			return "", "", e.New(e.CodeInvalidCatalog, "Helm index %s@%s requires the matching name and exactly one package URL", chart.Name, version)
		}
		rel, err := url.Parse(entry.URLs[0])
		if err != nil || entry.URLs[0] == "" {
			return "", "", e.New(e.CodeInvalidCatalog, "invalid Helm chart package URL")
		}
		location = base.ResolveReference(rel).String()
		if err := validateHelmHTTPS(location); err != nil {
			return "", "", err
		}
		if entry.Digest != "" {
			raw := strings.TrimPrefix(entry.Digest, "sha256:")
			if !hex64.MatchString(raw) {
				return "", "", e.New(e.CodeInvalidCatalog, "Helm index package digest must be SHA-256")
			}
			digest = "sha256:" + raw
		}
	}
	if matches != 1 {
		return "", "", e.New(e.CodeInvalidCatalog, "Helm index has %d entries for exact chart %s@%s; want one", matches, chart.Name, version)
	}
	return location, digest, nil
}

func readHelmOCI(ctx context.Context, chart HelmChart, version string, read helmOCIReader) (string, string, []byte, error) {
	ref, err := name.NewTag(strings.TrimRight(strings.TrimPrefix(chart.Repository, "oci://"), "/")+"/"+chart.Name+":"+strings.ReplaceAll(version, "+", "_"), name.StrictValidation)
	if err != nil {
		return "", "", nil, e.Wrap(e.CodeInvalidCatalog, err, "invalid Helm OCI reference")
	}
	image, err := read(ctx, ref)
	if err != nil {
		return "", "", nil, e.Wrap(e.CodeFetchFailed, err, "reading Helm OCI chart %s", ref)
	}
	manifest, err := image.Manifest()
	if err != nil {
		return "", "", nil, e.Wrap(e.CodeFetchFailed, err, "reading Helm OCI manifest")
	}
	if manifest.Config.MediaType != helmConfigType || len(manifest.Layers) != 1 || manifest.Layers[0].MediaType != helmContentType {
		return "", "", nil, e.New(e.CodeInvalidCatalog, "OCI chart must have Helm config and exactly one Helm chart-content layer")
	}
	descriptor := manifest.Layers[0]
	if descriptor.Digest.Algorithm != "sha256" || !hex64.MatchString(descriptor.Digest.Hex) || descriptor.Size <= 0 || descriptor.Size > maxHelmPackageBytes {
		return "", "", nil, e.New(e.CodeInvalidCatalog, "invalid Helm OCI layer digest or size")
	}
	layer, err := image.LayerByDigest(descriptor.Digest)
	if err != nil {
		return "", "", nil, e.Wrap(e.CodeFetchFailed, err, "reading Helm OCI layer")
	}
	reader, err := layer.Compressed()
	if err != nil {
		return "", "", nil, e.Wrap(e.CodeFetchFailed, err, "opening Helm OCI layer")
	}
	defer reader.Close()
	body, err := io.ReadAll(io.LimitReader(reader, maxHelmPackageBytes+1))
	if err != nil || int64(len(body)) != descriptor.Size {
		return "", "", nil, e.New(e.CodeFetchFailed, "Helm OCI layer read failed or size differs from descriptor: %v", err)
	}
	location := "https://" + ref.RegistryStr() + "/v2/" + ref.RepositoryStr() + "/blobs/" + descriptor.Digest.String()
	return location, descriptor.Digest.String(), body, nil
}

func validateHelmHTTPS(location string) error {
	u, err := url.Parse(location)
	if err != nil || u == nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return e.New(e.CodeInvalidCatalog, "Helm package URL must use HTTPS without embedded credentials or fragment")
	}
	return nil
}

func fetchHelmHTTPS(ctx context.Context, location string, limit int64) ([]byte, error) {
	if err := validateHelmHTTPS(location); err != nil {
		return nil, err
	}
	client := newPinHTTPClient()
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many Helm download redirects")
		}
		return validateHelmHTTPS(request.URL.String())
	}
	return fetchHelmWithClient(ctx, client, location, limit)
}

func fetchHelmWithClient(ctx context.Context, client *http.Client, location string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, e.Wrap(e.CodeFetchFailed, err, "creating Helm download request")
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, e.Wrap(e.CodeFetchFailed, err, "fetching Helm chart data")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > limit {
		return nil, e.New(e.CodeFetchFailed, "Helm download HTTP status %d or content length exceeds %d bytes", response.StatusCode, limit)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(body)) > limit || (response.ContentLength >= 0 && int64(len(body)) != response.ContentLength) {
		return nil, e.New(e.CodeFetchFailed, "Helm download exceeds bound or is truncated: %v", err)
	}
	return body, nil
}

// decodeHelmYAML allows upstream metadata while rejecting duplicate keys and
// multiple documents, which otherwise permit competing chart identities.
func decodeHelmYAML(raw []byte, target any) error {
	decoder := yamlv2.NewDecoder(bytes.NewReader(raw))
	decoder.SetStrict(true)
	var doc any
	if err := decoder.Decode(&doc); err != nil {
		return err
	}
	if err := decoder.Decode(&doc); err != io.EOF {
		return fmt.Errorf("expected exactly one YAML document")
	}
	encoded, err := yaml.YAMLToJSONStrict(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
}

func validateHelmArchive(ctx context.Context, archive []byte, chartName, version string) error {
	zipped, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "invalid Helm gzip archive")
	}
	defer func() { _ = zipped.Close() }()
	limited := &io.LimitedReader{R: zipped, N: maxHelmArchiveBytes + 1}
	reader := tar.NewReader(limited)
	seen := map[string]helmArchiveRecord{}
	found := false
	for count := 0; ; count++ {
		if err := pinContextError(ctx); err != nil {
			return err
		}
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return e.Wrap(e.CodeInvalidCatalog, err, "invalid Helm tar archive")
		}
		if count >= maxHelmFiles || limited.N <= 0 {
			return e.New(e.CodeInvalidCatalog, "Helm archive exceeds bounds")
		}
		entry := strings.TrimSuffix(header.Name, "/")
		if entry == "" || path.IsAbs(entry) || path.Clean(entry) != entry || strings.ContainsAny(entry, "\\\x00") || (entry != chartName && !strings.HasPrefix(entry, chartName+"/")) {
			return e.New(e.CodeInvalidCatalog, "unsafe Helm archive path %q", header.Name)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeDir {
			return e.New(e.CodeInvalidCatalog, "Helm archive entry %q is not a regular file or directory", header.Name)
		}
		if header.Size < 0 || header.Size > maxHelmArchiveBytes || (header.Typeflag == tar.TypeDir && header.Size != 0) {
			return e.New(e.CodeInvalidCatalog, "invalid Helm archive entry size")
		}
		previous, repeated := seen[entry]
		if repeated && (!helmRepeatedSubchartFile(chartName, entry) || header.Typeflag != tar.TypeReg ||
			previous.header.Typeflag != tar.TypeReg || !reflect.DeepEqual(previous.header, header)) {
			return e.New(e.CodeInvalidCatalog, "duplicate Helm archive path %q is not an identical regular subchart record", header.Name)
		}
		var digest [sha256.Size]byte
		if entry == chartName+"/Chart.yaml" {
			if header.Typeflag != tar.TypeReg || header.Size > maxHelmChartBytes {
				return e.New(e.CodeInvalidCatalog, "invalid Helm Chart.yaml type or size")
			}
			raw, err := io.ReadAll(reader)
			if err != nil {
				return e.Wrap(e.CodeInvalidCatalog, err, "reading Helm Chart.yaml")
			}
			var metadata struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			}
			if err := decodeHelmYAML(raw, &metadata); err != nil {
				return e.Wrap(e.CodeInvalidCatalog, err, "invalid Helm Chart.yaml")
			}
			if metadata.Name != chartName || metadata.Version != version {
				return e.New(e.CodeInvalidCatalog, "Helm package identity %s@%s does not match %s@%s", metadata.Name, metadata.Version, chartName, version)
			}
			digest = sha256.Sum256(raw)
			found = true
		} else {
			hash := sha256.New()
			if _, err := io.Copy(hash, reader); err != nil || limited.N <= 0 {
				return e.New(e.CodeInvalidCatalog, "Helm archive file is truncated or exceeds expanded size bound: %v", err)
			}
			copy(digest[:], hash.Sum(nil))
		}
		if repeated && digest != previous.digest {
			return e.New(e.CodeInvalidCatalog, "duplicate Helm subchart record %q has conflicting content", header.Name)
		}
		seen[entry] = helmArchiveRecord{header: header, digest: digest}
	}
	// Consume gzip through its checksum, also bounding data after the tar terminator.
	if _, err := io.Copy(helmPaddingWriter{}, limited); err != nil || limited.N <= 0 {
		return e.New(e.CodeInvalidCatalog, "Helm archive checksum invalid or expanded size exceeds bound: %v", err)
	}
	if !found {
		return e.New(e.CodeInvalidCatalog, "Helm package lacks root Chart.yaml")
	}
	return nil
}

type helmArchiveRecord struct {
	header *tar.Header
	digest [sha256.Size]byte
}

// Some published charts repeat vendored subchart records. Only regular files
// below charts/<subchart>/ may repeat, with all decoded tar metadata and payload
// bytes identical. Root metadata, directories, and other chart files stay unique.
// Every repeated record still consumes the ordinary entry and expanded-byte limits.
func helmRepeatedSubchartFile(chartName, entry string) bool {
	relative, ok := strings.CutPrefix(entry, chartName+"/charts/")
	if !ok {
		return false
	}
	subchart, file, ok := strings.Cut(relative, "/")
	return ok && file != "" && helmChartName.MatchString(subchart)
}

type helmPaddingWriter struct{}

func (helmPaddingWriter) Write(p []byte) (int, error) {
	for _, b := range p {
		if b != 0 {
			return 0, fmt.Errorf("non-padding data after Helm tar terminator")
		}
	}
	return len(p), nil
}

// The registry client may otherwise fall back to plain HTTP for local names.
// Bound every response, including manifest and token responses read by the library.
type helmTransport struct{ http.RoundTripper }

func (transport helmTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := validateHelmHTTPS(request.URL.String()); err != nil {
		return nil, err
	}
	response, err := transport.RoundTripper.RoundTrip(request)
	if err != nil {
		return nil, err
	}
	if response.ContentLength > maxHelmPackageBytes {
		_ = response.Body.Close()
		return nil, fmt.Errorf("helm OCI response exceeds size bound")
	}
	response.Body = &helmBoundedBody{ReadCloser: response.Body, remaining: maxHelmPackageBytes}
	return response, nil
}

type helmBoundedBody struct {
	io.ReadCloser
	remaining int64
}

func (body *helmBoundedBody) Read(p []byte) (int, error) {
	if int64(len(p)) > body.remaining+1 {
		p = p[:body.remaining+1]
	}
	n, err := body.ReadCloser.Read(p)
	body.remaining -= int64(n)
	if body.remaining < 0 {
		return 0, fmt.Errorf("helm OCI response exceeds size bound")
	}
	return n, err
}
