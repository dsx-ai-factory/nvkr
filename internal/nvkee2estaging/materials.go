// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkee2estaging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/dsx-ai-factory/nvkr/internal/securepath"
	"github.com/dsx-ai-factory/nvkr/internal/transportarchive"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/delivery"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/lifecycle"
	"golang.org/x/sys/unix"
)

// This helper receives only descriptions after Prepare has authenticated the
// full publication. Every projection independently revalidates its local bytes.
func stageMaterials(ctx context.Context, options Options, input mapping, named map[string]lifecycle.Artifact, layout string, publication *delivery.Publication, report *Report) error {
	site, err := section(input.SuiteConfig, "site", false)
	if err != nil {
		return err
	}
	cliConfig, err := section(input.SuiteConfig, "nvkectl", true)
	if err != nil {
		return err
	}
	cli, err := selected(named, input.Bindings.CLI, lifecycle.OutputExecutable)
	if err != nil {
		return err
	}
	binaries := filepath.Join(options.Out, "binaries")
	bundle, err := publication.ExtractBinaryBundle(ctx, cli.Name, binaries)
	if err != nil {
		return err
	}
	executable, err := bundleExecutable(bundle, input.Bindings.CLI.InstallPath, binaries, options.Arch)
	if err != nil {
		return err
	}
	payload, err := fileDigest(ctx, executable)
	if err != nil {
		return err
	}
	if err := putJSON(cliConfig, "binary", executable); err != nil {
		return err
	}
	report.Artifacts["cli"] = map[string]string{"trust": "publication-verified", "output": cli.Name, "subject": cli.Digest, "payload_digest": payload}
	images := map[string]any{}
	for _, role := range imageRoles {
		b := input.Bindings.Images[role]
		if b.Fixture != nil {
			archive, err := copyFixture(ctx, options.Out, role, *b.Fixture)
			if err != nil {
				return err
			}
			images[role] = map[string]string{"repository": b.Repository, "digest": b.Digest, "archive": archive}
			report.Artifacts[role] = map[string]string{"trust": "fixture", "fixture": b.Fixture.Identity, "payload_digest": b.Fixture.SHA256}
			continue
		}
		output, err := selected(named, b, lifecycle.OutputOCI)
		if err != nil {
			return err
		}
		native := output.Digest
		if output.ImageIndex != nil {
			matches := 0
			for _, child := range output.ImageIndex.Children {
				if child.Platform == "linux/"+options.Arch {
					native = child.ManifestDigest
					matches++
				}
			}
			if matches != 1 {
				return e.New(e.CodeVerifyFailed, "image output does not contain exactly one selected native platform")
			}
		}
		repository, _, found := strings.Cut(output.Reference, "@")
		if !found {
			return e.New(e.CodeVerifyFailed, "image output omits its immutable publication reference")
		}
		reference := repository + "@" + native
		archive := filepath.Join(options.Out, role+".tar")
		image, err := delivery.ExportPublishedImage(ctx, layout, output.Name, reference, "linux/"+options.Arch, archive)
		if err != nil {
			return err
		}
		if image.ManifestDigest != native || image.Reference != reference {
			return e.New(e.CodeVerifyFailed, "image export substituted native subject")
		}
		images[role] = map[string]string{"repository": repository, "digest": image.ManifestDigest, "archive": archive}
		report.Artifacts[role] = map[string]string{"trust": "publication-verified", "output": output.Name, "subject": output.Digest, "payload_digest": image.ArchiveDigest}
	}
	overrides := map[string]string{}
	for _, role := range chartRoles {
		b := input.Bindings.Charts[role]
		var chart string
		if b.Fixture != nil {
			chart, err = copyFixture(ctx, options.Out, "chart-"+role, *b.Fixture)
			if err != nil {
				return err
			}
			report.Artifacts["chart:"+role] = map[string]string{"trust": "fixture", "fixture": b.Fixture.Identity, "payload_digest": b.Fixture.SHA256}
		} else {
			output, err := selected(named, b, lifecycle.OutputHelm)
			if err != nil {
				return err
			}
			chart = filepath.Join(options.Out, "chart-"+role+".tgz")
			if _, err := publication.ExtractObject(ctx, output.Name, chart); err != nil {
				return err
			}
			digest, err := fileDigest(ctx, chart)
			if err != nil {
				return err
			}
			report.Artifacts["chart:"+role] = map[string]string{"trust": "publication-verified", "output": output.Name, "subject": output.Digest, "payload_digest": digest}
		}
		overrides[role] = chart
	}
	if err := putJSON(site, "images", images); err != nil {
		return err
	}
	if err := putJSON(site, "chartOverrides", overrides); err != nil {
		return err
	}
	if err := putJSON(input.SuiteConfig, "site", site); err != nil {
		return err
	}
	if err := putJSON(input.SuiteConfig, "nvkectl", cliConfig); err != nil {
		return err
	}
	var commit string
	if err := json.Unmarshal(site["deployPathRef"], &commit); err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "reading deployment source binding")
	}
	identity := input.DeployFixture.Identity
	if identity == "" {
		identity = "checked harness source against unsigned receipt"
	}
	trust := "source-checked-unsigned-receipt"
	if input.DeployFixture.Identity != "" {
		trust = "fixture"
	}
	report.Artifacts["deploy_assets"] = map[string]string{"trust": trust, "commit": commit, "fixture": identity}
	return writeJSON(ctx, options.Out, "e2e-config.json", input.SuiteConfig)
}

func bundleExecutable(bundle delivery.BinaryBundle, installPath, binaries, targetArch string) (string, error) {
	// The site-controller suite uploads this CLI onto the provisioned site node
	// and runs it remotely. The Go harness host architecture is independent.
	if bundle.OS != "linux" || bundle.Arch != targetArch {
		return "", e.New(e.CodeEngineCapability, "selected CLI platform differs from the Linux site node")
	}
	var selected string
	matches := 0
	for _, entry := range bundle.Entries {
		if entry.InstallPath == installPath {
			if entry.Mode&0o111 == 0 {
				return "", e.New(e.CodeVerifyFailed, "selected CLI entry is not executable")
			}
			selected = entry.Name
			matches++
		}
	}
	if matches != 1 || selected == "" || selected == "." || selected == ".." || strings.ContainsAny(selected, "/\\\x00") {
		return "", e.New(e.CodeVerifyFailed, "CLI installation path must identify exactly one flat verified bundle entry")
	}
	return filepath.Join(binaries, selected), nil
}

func putJSON(object map[string]json.RawMessage, key string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "encoding downstream input")
	}
	object[key] = raw
	return nil
}

func fileDigest(ctx context.Context, filename string) (string, error) {
	file, info, err := securepath.OpenRegular(filename)
	if err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "opening staged payload")
	}
	defer file.Close()
	if info.Size() < 0 || info.Size() > fixtureLimit {
		return "", e.New(e.CodeVerifyFailed, "staged payload exceeds its byte bound")
	}
	hash := sha256.New()
	if _, err := transportarchive.CopyExact(ctx, hash, file, info.Size()); err != nil {
		return "", inputError(ctx, e.CodeVerifyFailed, err, "hashing staged payload")
	}
	after, err := file.Stat()
	if err != nil || !securepath.SameMetadata(info, after) {
		return "", e.New(e.CodeVerifyFailed, "staged payload changed while hashing")
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func copyFixture(ctx context.Context, out, role string, input fixture) (string, error) {
	file, info, err := securepath.OpenRegular(input.Path)
	if err != nil {
		return "", e.Wrap(e.CodeVerifyFailed, err, "opening regular non-symlink fixture")
	}
	defer file.Close()
	if info.Size() < 0 || info.Size() > fixtureLimit {
		return "", e.New(e.CodeVerifyFailed, "fixture exceeds its byte bound")
	}
	directory := filepath.Join(out, "fixtures")
	if err := os.Mkdir(directory, 0700); err != nil && !os.IsExist(err) {
		return "", e.Wrap(e.CodeInternal, err, "creating private fixture directory")
	}
	parent, _, err := securepath.OpenDirectory(directory)
	if err != nil {
		return "", e.Wrap(e.CodeInternal, err, "holding fixture destination")
	}
	defer parent.Close()
	target, temporary, err := securepath.CreateTemporarySibling(parent)
	if err != nil {
		return "", e.Wrap(e.CodeInternal, err, "opening private fixture copy")
	}
	defer func() { _ = target.Close(); _ = unix.Unlinkat(int(parent.Fd()), temporary, 0) }()
	hash := sha256.New()
	if _, err := transportarchive.CopyExact(ctx, io.MultiWriter(target, hash), file, info.Size()); err != nil {
		return "", inputError(ctx, e.CodeVerifyFailed, err, "capturing exact fixture bytes")
	}
	after, err := file.Stat()
	if err != nil || !securepath.SameMetadata(info, after) {
		return "", e.New(e.CodeVerifyFailed, "fixture changed while copying")
	}
	if "sha256:"+hex.EncodeToString(hash.Sum(nil)) != input.SHA256 {
		return "", e.New(e.CodeVerifyFailed, "fixture payload differs from its explicit byte pin")
	}
	if err := target.Sync(); err != nil {
		return "", e.Wrap(e.CodeInternal, err, "persisting private fixture")
	}
	if err := transportarchive.PublishAtNoReplace(int(parent.Fd()), temporary, int(parent.Fd()), role); err != nil {
		return "", e.Wrap(e.CodeImmutableViolation, err, "committing fixture without replacement")
	}
	if err := parent.Sync(); err != nil {
		return "", e.Wrap(e.CodeInternal, err, "persisting fixture commit")
	}
	return filepath.Join(directory, role), nil
}

func writeJSON(ctx context.Context, directory, name string, value any) error {
	raw, err := jsonBytes(value)
	if err != nil {
		return err
	}
	parent, _, err := securepath.OpenDirectory(directory)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "holding preparation directory")
	}
	defer parent.Close()
	file, temporary, err := securepath.CreateTemporarySibling(parent)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "opening preparation description")
	}
	defer func() { _ = file.Close(); _ = unix.Unlinkat(int(parent.Fd()), temporary, 0) }()
	if _, err := transportarchive.CopyExact(ctx, file, strings.NewReader(string(raw)), int64(len(raw))); err != nil {
		return inputError(ctx, e.CodeInternal, err, "writing preparation description")
	}
	if err := file.Sync(); err != nil {
		return e.Wrap(e.CodeInternal, err, "persisting preparation description")
	}
	if err := transportarchive.PublishAtNoReplace(int(parent.Fd()), temporary, int(parent.Fd()), name); err != nil {
		return e.Wrap(e.CodeImmutableViolation, err, "committing preparation description")
	}
	return wrapIO(parent.Sync())
}

func wrapIO(err error) error {
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "persisting preparation directory")
	}
	return nil
}
