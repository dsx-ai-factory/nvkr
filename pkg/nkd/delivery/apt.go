package delivery

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/internal/heldfile"
)

const APTRepositorySchema = "nkd.dev/apt-repository/v1"

type DebianPackageInput struct {
	Path string
}

type DebianPackage struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
	Filename     string `json:"filename"`
	SHA256       string `json:"sha256"`
	Size         int64  `json:"size"`
	Description  string `json:"description,omitempty"`
}

type APTRepository struct {
	SchemaVersion    string          `json:"schemaVersion"`
	Distribution     string          `json:"distribution"`
	Component        string          `json:"component"`
	ReleaseSetDigest string          `json:"releaseSetDigest,omitempty"`
	InventoryDigest  string          `json:"inventoryDigest,omitempty"`
	Packages         []DebianPackage `json:"packages"`
}

func (repository APTRepository) Validate() error {
	if repository.SchemaVersion != APTRepositorySchema || !canonicalToken(repository.Distribution) || !canonicalToken(repository.Component) {
		return e.New(e.CodeVerifyFailed, "APT repository schema/distribution/component is invalid")
	}
	if (repository.ReleaseSetDigest == "") != (repository.InventoryDigest == "") ||
		(repository.ReleaseSetDigest != "" && (!isDigest(repository.ReleaseSetDigest) || !isDigest(repository.InventoryDigest))) {
		return e.New(e.CodeUnverifiedRef, "APT repository release/inventory identities must be supplied together")
	}
	if len(repository.Packages) == 0 || len(repository.Packages) > maxDeliveryObjects {
		return e.New(e.CodeReleaseIncomplete, "APT repository requires 1..%d packages", maxDeliveryObjects)
	}
	previous := ""
	filenames := map[string]bool{}
	for _, pkg := range repository.Packages {
		key := pkg.Name + "\x00" + pkg.Version + "\x00" + pkg.Architecture
		if !canonicalToken(pkg.Name) || !canonicalToken(pkg.Version) || !canonicalToken(pkg.Architecture) || key <= previous ||
			!isDigest(pkg.SHA256) || pkg.Size < 0 || !safeRelativePath(pkg.Filename) || filenames[pkg.Filename] {
			return e.New(e.CodeVerifyFailed, "APT packages must be canonical, sorted, unique, regular-file bindings")
		}
		filenames[pkg.Filename] = true
		previous = key
	}
	return nil
}

func (repository APTRepository) Digest() (string, error) {
	if err := repository.Validate(); err != nil {
		return "", err
	}
	return canonical.Digest(canonical.KindAPTRepository, repository)
}

type APTRequest struct {
	Distribution     string
	Component        string
	Epoch            int64
	ReleaseSetDigest string
	InventoryDigest  string
	Packages         []DebianPackageInput
}

type APTResult struct {
	Repository APTRepository
	Digest     string
	Path       string
}

type heldDebianPackage struct {
	file    *heldfile.File
	control map[string]string
	entry   DebianPackage
}

// BuildAPTRepository emits a deterministic, Acquire-By-Hash Debian repository
// over exact .deb files. It is intentionally unsigned local projection metadata;
// consumers must either use an authenticated enclosing publication root or opt in
// to apt's trusted=yes only for a loopback development repository.
func BuildAPTRepository(ctx context.Context, out string, request APTRequest) (APTResult, error) {
	if err := deliveryContext(ctx); err != nil {
		return APTResult{}, err
	}
	if len(request.Packages) == 0 || len(request.Packages) > maxDeliveryObjects {
		return APTResult{}, e.New(e.CodeUsage, "APT projection requires 1..%d .deb files", maxDeliveryObjects)
	}
	heldPackages := make([]heldDebianPackage, 0, len(request.Packages))
	defer func() {
		for i := range heldPackages {
			_ = heldPackages[i].file.Close()
		}
	}()
	for _, input := range request.Packages {
		held, err := heldfile.OpenContext(ctx, input.Path, heldfile.Options{Label: "Debian package", MaxBytes: maxDeliveryFileBytes})
		if err != nil {
			return APTResult{}, classifyContext(ctx, e.Wrap(e.CodeVerifyFailed, err, "capturing Debian package"))
		}
		control, err := readDebianControl(ctx, held)
		if err != nil {
			_ = held.Close()
			return APTResult{}, err
		}
		name, version, arch := control["Package"], control["Version"], control["Architecture"]
		if !debianToken(name) || !debianToken(version) || !debianToken(arch) {
			_ = held.Close()
			return APTResult{}, e.New(e.CodeVerifyFailed, "Debian package control lacks canonical Package/Version/Architecture")
		}
		filename := filepath.ToSlash(filepath.Join("pool", request.Component, name[:1], name,
			fmt.Sprintf("%s_%s_%s.deb", name, version, arch)))
		heldPackages = append(heldPackages, heldDebianPackage{file: held, control: control,
			entry: DebianPackage{Name: name, Version: version, Architecture: arch, Filename: filename,
				SHA256: held.Digest(), Size: held.Size(), Description: firstControlLine(control["Description"])}})
	}
	sort.Slice(heldPackages, func(i, j int) bool {
		left, right := heldPackages[i].entry, heldPackages[j].entry
		if left.Name != right.Name {
			return left.Name < right.Name
		}
		if left.Version != right.Version {
			return left.Version < right.Version
		}
		return left.Architecture < right.Architecture
	})
	repository := APTRepository{SchemaVersion: APTRepositorySchema, Distribution: request.Distribution,
		Component: request.Component, ReleaseSetDigest: request.ReleaseSetDigest,
		InventoryDigest: request.InventoryDigest, Packages: make([]DebianPackage, len(heldPackages))}
	for i := range heldPackages {
		repository.Packages[i] = heldPackages[i].entry
	}
	if err := repository.Validate(); err != nil {
		return APTResult{}, err
	}
	digest, err := repository.Digest()
	if err != nil {
		return APTResult{}, err
	}
	stage, commit, cleanup, err := stageLayout(out)
	if err != nil {
		return APTResult{}, err
	}
	defer cleanup()
	for i := range heldPackages {
		pkg := &heldPackages[i]
		destination := filepath.Join(stage, filepath.FromSlash(pkg.entry.Filename))
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return APTResult{}, err
		}
		file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return APTResult{}, err
		}
		_, copyErr := pkg.file.WriteToContext(ctx, file)
		if copyErr == nil {
			copyErr = file.Sync()
		}
		closeErr := file.Close()
		if copyErr != nil {
			return APTResult{}, classifyContext(ctx, copyErr)
		}
		if closeErr != nil {
			return APTResult{}, closeErr
		}
		if err := pkg.file.AssertUnchangedContext(ctx); err != nil {
			return APTResult{}, err
		}
	}
	architectures := map[string]bool{}
	for _, pkg := range repository.Packages {
		architectures[pkg.Architecture] = true
	}
	archList := sortedMapKeys(architectures)
	metadata := map[string][]byte{}
	for _, arch := range archList {
		var packages bytes.Buffer
		for _, held := range heldPackages {
			if held.entry.Architecture != arch {
				continue
			}
			writePackageParagraph(&packages, held.control, held.entry)
		}
		rel := filepath.ToSlash(filepath.Join(request.Component, "binary-"+arch, "Packages"))
		metadata[rel] = packages.Bytes()
		metadata[rel+".gz"] = deterministicGzip(packages.Bytes())
	}
	for rel, raw := range metadata {
		path := filepath.Join(stage, "dists", request.Distribution, filepath.FromSlash(rel))
		if err := writeMetadata(path, raw); err != nil {
			return APTResult{}, err
		}
		byHash := filepath.Join(filepath.Dir(path), "by-hash", "SHA256", strings.TrimPrefix(digestBytes(raw), "sha256:"))
		if err := writeMetadata(byHash, raw); err != nil {
			return APTResult{}, err
		}
	}
	releaseBytes := buildAPTRelease(request, archList, metadata)
	if err := writeMetadata(filepath.Join(stage, "dists", request.Distribution, "Release"), releaseBytes); err != nil {
		return APTResult{}, err
	}
	manifestBytes, err := canonical.Marshal(repository)
	if err != nil {
		return APTResult{}, err
	}
	if err := writeMetadata(filepath.Join(stage, "nkd-apt-repository.json"), manifestBytes); err != nil {
		return APTResult{}, err
	}
	if err := commit(); err != nil {
		return APTResult{}, err
	}
	return APTResult{Repository: repository, Digest: digest, Path: out}, nil
}

// InspectAPTRepository independently reopens the canonical manifest and every
// package payload in a materialized repository.
func InspectAPTRepository(ctx context.Context, root string) (APTResult, error) {
	manifest, err := heldfile.OpenContext(ctx, filepath.Join(root, "nkd-apt-repository.json"),
		heldfile.Options{Label: "APT repository manifest", MaxBytes: maxDeliveryFileBytes})
	if err != nil {
		return APTResult{}, e.Wrap(e.CodeVerifyFailed, err, "opening APT repository manifest")
	}
	defer func() { _ = manifest.Close() }()
	raw, err := manifest.ReadAllContext(ctx, maxDeliveryFileBytes)
	if err != nil {
		return APTResult{}, err
	}
	var repository APTRepository
	if err := canonical.UnmarshalExact(raw, &repository); err != nil {
		return APTResult{}, e.Wrap(e.CodeVerifyFailed, err, "decoding APT repository manifest")
	}
	digest, err := repository.Digest()
	if err != nil {
		return APTResult{}, err
	}
	for _, pkg := range repository.Packages {
		held, err := heldfile.OpenContext(ctx, filepath.Join(root, filepath.FromSlash(pkg.Filename)), heldfile.Options{Label: "APT package", MaxBytes: maxDeliveryFileBytes})
		if err != nil {
			return APTResult{}, e.Wrap(e.CodeVerifyFailed, err, "opening APT package %q", pkg.Name)
		}
		matches := held.Digest() == pkg.SHA256 && held.Size() == pkg.Size
		closeErr := held.Close()
		if !matches || closeErr != nil {
			return APTResult{}, e.New(e.CodeVerifyFailed, "APT package %q differs from its canonical manifest", pkg.Name)
		}
	}
	return APTResult{Repository: repository, Digest: digest, Path: root}, nil
}

func readDebianControl(ctx context.Context, held *heldfile.File) (map[string]string, error) {
	reader := io.NewSectionReader(held, 0, held.Size())
	magic := make([]byte, 8)
	if _, err := io.ReadFull(reader, magic); err != nil || string(magic) != "!<arch>\n" {
		return nil, e.New(e.CodeVerifyFailed, "input is not a Debian ar archive")
	}
	offset := int64(8)
	for offset+60 <= held.Size() {
		if err := deliveryContext(ctx); err != nil {
			return nil, err
		}
		header := make([]byte, 60)
		if _, err := held.ReadAt(header, offset); err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "reading Debian ar header")
		}
		if string(header[58:60]) != "`\n" {
			return nil, e.New(e.CodeVerifyFailed, "Debian ar member header is malformed")
		}
		name := strings.TrimSuffix(strings.TrimSpace(string(header[0:16])), "/")
		size, err := strconv.ParseInt(strings.TrimSpace(string(header[48:58])), 10, 64)
		if err != nil || size < 0 || offset+60+size > held.Size() {
			return nil, e.New(e.CodeVerifyFailed, "Debian ar member size is malformed")
		}
		dataOffset := offset + 60
		if name == "control.tar.gz" {
			gzipReader, err := gzip.NewReader(io.NewSectionReader(held, dataOffset, size))
			if err != nil {
				return nil, e.Wrap(e.CodeVerifyFailed, err, "opening control.tar.gz")
			}
			control, readErr := readControlTar(ctx, gzipReader)
			closeErr := gzipReader.Close()
			if readErr != nil {
				return nil, readErr
			}
			if closeErr != nil {
				return nil, closeErr
			}
			return parseControl(control)
		}
		offset = dataOffset + size
		if offset%2 != 0 {
			offset++
		}
	}
	return nil, e.New(e.CodeVerifyFailed, "Debian package has no supported control.tar.gz member")
}

func readControlTar(ctx context.Context, reader io.Reader) ([]byte, error) {
	tarReader := tar.NewReader(contextReader{ctx: ctx, reader: reader})
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, e.Wrap(e.CodeVerifyFailed, err, "reading Debian control archive")
		}
		name := strings.TrimPrefix(header.Name, "./")
		if name == "control" && header.Typeflag == tar.TypeReg {
			if header.Size < 0 || header.Size > 1<<20 {
				return nil, e.New(e.CodeVerifyFailed, "Debian control file exceeds 1 MiB")
			}
			raw := make([]byte, header.Size)
			if _, err := io.ReadFull(tarReader, raw); err != nil {
				return nil, err
			}
			return raw, nil
		}
	}
	return nil, e.New(e.CodeVerifyFailed, "Debian package has no control file")
}

func parseControl(raw []byte) (map[string]string, error) {
	fields := map[string]string{}
	var current string
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if current == "" {
				return nil, e.New(e.CodeVerifyFailed, "Debian control continuation has no field")
			}
			fields[current] += "\n" + strings.TrimSpace(line)
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok || !canonicalToken(name) {
			return nil, e.New(e.CodeVerifyFailed, "Debian control line is malformed")
		}
		current = name
		fields[name] = strings.TrimSpace(value)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return fields, nil
}

func writePackageParagraph(out *bytes.Buffer, control map[string]string, entry DebianPackage) {
	order := []string{"Package", "Version", "Architecture", "Maintainer", "Section", "Priority", "Description"}
	for _, name := range order {
		if value := control[name]; value != "" {
			lines := strings.Split(value, "\n")
			fmt.Fprintf(out, "%s: %s\n", name, lines[0])
			for _, line := range lines[1:] {
				fmt.Fprintf(out, " %s\n", line)
			}
		}
	}
	fmt.Fprintf(out, "Filename: %s\nSize: %d\nSHA256: %s\n\n", entry.Filename, entry.Size, strings.TrimPrefix(entry.SHA256, "sha256:"))
}

func buildAPTRelease(request APTRequest, architectures []string, metadata map[string][]byte) []byte {
	var out bytes.Buffer
	date := time.Unix(request.Epoch, 0).UTC().Format(time.RFC1123)
	fmt.Fprintf(&out, "Origin: NKD\nLabel: NKD local projection\nSuite: %s\nCodename: %s\nDate: %s\nArchitectures: %s\nComponents: %s\nAcquire-By-Hash: yes\nSHA256:\n",
		request.Distribution, request.Distribution, date, strings.Join(architectures, " "), request.Component)
	paths := make([]string, 0, len(metadata))
	for path := range metadata {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		raw := metadata[path]
		fmt.Fprintf(&out, " %s %d %s\n", strings.TrimPrefix(digestBytes(raw), "sha256:"), len(raw), path)
	}
	return out.Bytes()
}

func deterministicGzip(raw []byte) []byte {
	var out bytes.Buffer
	writer, _ := gzip.NewWriterLevel(&out, gzip.BestCompression)
	writer.ModTime = time.Time{}
	writer.OS = 255
	_, _ = writer.Write(raw)
	_ = writer.Close()
	return out.Bytes()
}

func writeMetadata(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func sortedMapKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func debianToken(value string) bool {
	return canonicalToken(value) && !strings.ContainsAny(value, " /\\")
}

func firstControlLine(value string) string {
	if before, _, ok := strings.Cut(value, "\n"); ok {
		return before
	}
	return value
}

func safeRelativePath(value string) bool {
	if value == "" || filepath.IsAbs(value) || strings.Contains(value, "\\") {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	return clean == value && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}
