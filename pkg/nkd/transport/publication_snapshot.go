package transport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// snapshotPublication freezes every publication input in a private directory.
// Adapters receive only these paths, never paths controlled by the caller.
func snapshotPublication(ctx context.Context, artifacts []Artifact) ([]Artifact, func(), error) {
	if err := transportContextErr(ctx); err != nil {
		return nil, func() {}, err
	}
	root, err := os.MkdirTemp("", "nkd-publication-snapshot-")
	if err != nil {
		return nil, func() {}, e.Wrap(e.CodeInternal, err, "creating private publication snapshot")
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	snapshots := make([]Artifact, len(artifacts))
	for artifactIndex, artifact := range artifacts {
		if err := transportContextErr(ctx); err != nil {
			cleanup()
			return nil, func() {}, err
		}
		directory := filepath.Join(root, fmt.Sprintf("artifact-%06d", artifactIndex))
		if err := os.Mkdir(directory, 0o700); err != nil {
			cleanup()
			return nil, func() {}, e.Wrap(e.CodeInternal, err, "creating publication snapshot directory")
		}
		copyArtifact := artifact
		copyArtifact.Referrers = append([]Referrer(nil), artifact.Referrers...)
		copyArtifact.Path = filepath.Join(directory, filepath.Base(artifact.Path))
		if err := snapshotPublicationFile(ctx, artifact.Path, copyArtifact.Path, artifact.Digest,
			fmt.Sprintf("artifact %q", artifact.Name)); err != nil {
			cleanup()
			return nil, func() {}, err
		}
		for referrerIndex, referrer := range artifact.Referrers {
			referrerDirectory := filepath.Join(directory, fmt.Sprintf("referrer-%06d", referrerIndex))
			if err := os.Mkdir(referrerDirectory, 0o700); err != nil {
				cleanup()
				return nil, func() {}, e.Wrap(e.CodeInternal, err, "creating referrer snapshot directory")
			}
			copyArtifact.Referrers[referrerIndex].Path = filepath.Join(referrerDirectory, filepath.Base(referrer.Path))
			if err := snapshotPublicationFile(ctx, referrer.Path, copyArtifact.Referrers[referrerIndex].Path,
				referrer.Digest, fmt.Sprintf("referrer %q", referrer.ArtifactType)); err != nil {
				cleanup()
				return nil, func() {}, err
			}
		}
		snapshots[artifactIndex] = copyArtifact
	}
	return snapshots, cleanup, nil
}

func snapshotPublicationFile(ctx context.Context, sourcePath, snapshotPath, expectedDigest, label string) error {
	source, err := openNoFollowRegular(sourcePath)
	if err != nil {
		return e.Wrap(e.CodeVerifyFailed, err, "opening %s as a no-follow regular file", label)
	}
	defer source.Close()

	destination, err := os.OpenFile(snapshotPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "creating private snapshot for %s", label)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(destination, hash), contextReader{ctx: ctx, reader: source})
	if copyErr == nil {
		copyErr = destination.Sync()
	}
	closeErr := destination.Close()
	if copyErr != nil {
		if ctx.Err() != nil {
			return transportContextErr(ctx)
		}
		return e.Wrap(e.CodeVerifyFailed, copyErr, "snapshotting %s", label)
	}
	if closeErr != nil {
		return e.Wrap(e.CodeInternal, closeErr, "closing private snapshot for %s", label)
	}
	got := "sha256:" + hex.EncodeToString(hash.Sum(nil))
	if got != expectedDigest {
		return e.New(e.CodeVerifyFailed, "%s changed after authorization: expected %s, got %s", label, expectedDigest, got)
	}
	if err := os.Chmod(snapshotPath, 0o400); err != nil {
		return e.Wrap(e.CodeInternal, err, "making private snapshot read-only for %s", label)
	}
	return nil
}
