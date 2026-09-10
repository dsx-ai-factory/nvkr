package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/status"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/trust"
	"github.com/urfave/cli/v2"
	"golang.org/x/sys/unix"
)

const (
	statusInputsSchemaVersion    = "nkd.dev/status-verification-inputs/v1"
	statusHighWaterSchemaVersion = "nkd.dev/status-high-water/v1"
)

// signedDocumentLocation is transport configuration, not trust evidence. The
// document authenticator snapshots and verifies every referenced file before a
// path can become a trust.DocumentReceipt.
type signedDocumentLocation struct {
	Document    string `json:"document"`
	Signature   string `json:"signature,omitempty"`
	Certificate string `json:"certificate"`
	Bundle      string `json:"bundle,omitempty"`
	TrustedRoot string `json:"trustedRoot,omitempty"`
}

type statusVerificationInputs struct {
	SchemaVersion string                   `json:"schemaVersion"`
	Checkpoint    signedDocumentLocation   `json:"checkpoint"`
	Declarations  []signedDocumentLocation `json:"declarations"`
}

type statusHighWaterFile struct {
	SchemaVersion    string `json:"schemaVersion"`
	Target           string `json:"target"`
	Authority        string `json:"authority"`
	Sequence         uint64 `json:"sequence"`
	CheckpointDigest string `json:"checkpointDigest"`
}

func releaseStatusCmd() *cli.Command {
	return releaseStatusCmdWithVerifier(nil)
}

func releaseStatusCmdWithVerifier(verifier trust.DocumentVerifier) *cli.Command {
	return &cli.Command{
		Name:  "status",
		Usage: "diagnostically authenticate a status checkpoint at a caller epoch and update its high-water mark (not release clearance)",
		Flags: append([]cli.Flag{
			&cli.StringFlag{Name: "target", Required: true, Usage: "exact publication digest governed by the checkpoint"},
			&cli.StringFlag{Name: "authority", Required: true, Usage: "stable status authority identity"},
			&cli.StringFlag{Name: "documents", Required: true, Usage: "canonical JSON locating the checkpoint and its exact declaration set"},
			&cli.StringFlag{Name: "high-water", Required: true, Usage: "canonical authenticated local high-water policy state"},
			&cli.StringFlag{Name: "high-water-out", Usage: "atomically persist the accepted high-water state (defaults to --high-water)"},
			&cli.Int64Flag{Name: "as-of", Required: true, Usage: "caller-supplied diagnostic policy epoch; not trusted-time authority"},
			&cli.StringSliceFlag{Name: "status-identity", Usage: "currently authorized status certificate identity (repeatable; overrides trust store)"},
			&cli.StringFlag{Name: "status-issuer", Required: true, Usage: "exact pinned status OIDC issuer"},
			&cli.StringFlag{Name: "trust-store", Usage: "role-separated operator trust store (default .nkd/trust.json)"},
			&cli.StringFlag{Name: "trusted-root-digest", Usage: "exact lowercase sha256 policy pin for every offline status trusted root"},
			&cli.BoolFlag{Name: "offline", Usage: "require a local Sigstore bundle and explicit trusted root for every status document"},
		}, qualifiedDocumentVerifierFlags()...),
		Action: func(c *cli.Context) error {
			return consumeQualifiedStatus(c, verifier)
		},
	}
}

func consumeQualifiedStatus(c *cli.Context, verifier trust.DocumentVerifier) error {
	documentVerifier, err := resolveQualifiedDocumentVerifier(c, verifier)
	if err != nil {
		return err
	}
	var inputs statusVerificationInputs
	if err := readCanonicalJSONContext(c.Context, c.String("documents"), &inputs); err != nil {
		return err
	}
	if inputs.SchemaVersion != statusInputsSchemaVersion {
		return e.New(e.CodeInvalidCatalog, "status input schemaVersion %q is unsupported (want %q)", inputs.SchemaVersion, statusInputsSchemaVersion)
	}
	if err := validateSignedDocumentLocation("status checkpoint", inputs.Checkpoint, c.Bool("offline")); err != nil {
		return err
	}
	for i, declaration := range inputs.Declarations {
		if err := validateSignedDocumentLocation(fmt.Sprintf("status declaration %d", i), declaration, c.Bool("offline")); err != nil {
			return err
		}
	}

	var highWater statusHighWaterFile
	highWaterRaw, err := readProtectedHighWaterContext(c.Context, c.String("high-water"), &highWater)
	if err != nil {
		return err
	}
	if highWater.SchemaVersion != statusHighWaterSchemaVersion {
		return e.New(e.CodeInvalidCatalog, "status high-water schemaVersion %q is unsupported (want %q)", highWater.SchemaVersion, statusHighWaterSchemaVersion)
	}
	if highWater.Target != c.String("target") || highWater.Authority != c.String("authority") {
		return e.New(e.CodeStatusCheckpoint, "status high-water target/authority does not match command policy")
	}

	identities, err := qualifiedStatusIdentities(c)
	if err != nil {
		return err
	}
	rolePolicy, err := trust.NewRolePolicy(trust.RoleStatus, trust.Store{CLIIdentities: identities}, c.String("status-issuer"))
	if err != nil {
		return err
	}
	statusRootPath := inputs.Checkpoint.TrustedRoot
	for _, declaration := range inputs.Declarations {
		if statusRootPath == "" {
			statusRootPath = declaration.TrustedRoot
		}
	}
	rolePolicy, err = qualifiedPolicyWithTrustedRoot(rolePolicy,
		statusRootPath, c.String("trusted-root-digest"), c.Bool("offline"))
	if err != nil {
		return err
	}
	authenticator, err := trust.NewDocumentAuthenticator(documentVerifier,
		trust.JSONSchema[status.StatusCheckpoint](status.CheckpointSchemaVersion),
		trust.JSONSchema[status.StatusDeclaration](status.SchemaVersion),
	)
	if err != nil {
		return err
	}
	checkpointReceipt, err := authenticateStatusLocation(c.Context, authenticator, rolePolicy, inputs.Checkpoint, c.Bool("offline"))
	if err != nil {
		return err
	}
	declarations := make([]trust.DocumentReceipt, 0, len(inputs.Declarations))
	for _, location := range inputs.Declarations {
		receipt, err := authenticateStatusLocation(c.Context, authenticator, rolePolicy, location, c.Bool("offline"))
		if err != nil {
			return err
		}
		declarations = append(declarations, receipt)
	}

	checkpointPolicy, err := status.NewCheckpointPolicy(c.String("target"), c.String("authority"), c.String("status-issuer"), identities)
	if err != nil {
		return err
	}
	minimum, err := status.NewHighWaterMark(highWater.Target, highWater.Authority, highWater.Sequence, highWater.CheckpointDigest)
	if err != nil {
		return err
	}
	asOf, err := status.At(c.Int64("as-of"))
	if err != nil {
		return err
	}
	verified, err := status.VerifyCheckpoint(c.Context, status.CheckpointRequest{
		Policy: checkpointPolicy, HighWater: minimum, AsOf: asOf,
		Checkpoint: checkpointReceipt, Declarations: declarations,
	})
	if err != nil {
		return err
	}
	next, err := statusHighWaterFromReceipt(verified)
	if err != nil {
		return err
	}
	path := c.String("high-water-out")
	if path == "" {
		path = c.String("high-water")
	}
	if filepath.Clean(path) != filepath.Clean(c.String("high-water")) {
		return e.New(e.CodeUsage,
			"--high-water-out must equal --high-water so monotonic state is updated with compare-and-swap")
	}
	encoded, err := canonical.Marshal(next)
	if err != nil {
		return err
	}
	if err := persistStatusHighWaterContext(c.Context, path, highWaterRaw, encoded); err != nil {
		return err
	}
	// Persist the authenticated checkpoint before interpreting a withdrawn
	// declaration. Otherwise a correct fail-closed withdrawal would leave the old
	// high-water in place and allow a later invocation to replay an older view.
	decision, err := verified.Decision()
	if err != nil {
		return err
	}
	digest, _ := verified.CheckpointDigest()
	sequence, _ := verified.Sequence()
	fmt.Printf("UNQUALIFIED DIAGNOSTIC STATUS: checkpoint %s sequence %d was authenticated at caller-supplied epoch %d; no trusted-time binding or clearance authority was issued\n",
		digest, sequence, c.Int64("as-of"))
	if !decision.Effective {
		fmt.Println("no declaration is in effect at that diagnostic epoch")
		return nil
	}
	fmt.Printf("diagnostic decision: %s: %s\n", decision.Action, decision.Warn)
	return nil
}

func authenticateStatusLocation(ctx context.Context, authenticator *trust.DocumentAuthenticator,
	policy trust.RolePolicy, location signedDocumentLocation, offline bool,
) (trust.DocumentReceipt, error) {
	return authenticator.Authenticate(ctx, trust.AuthenticateRequest{
		SourcePath: location.Document,
		Signature: trust.SignatureMaterial{
			SignaturePath: location.Signature, CertificatePath: location.Certificate,
			BundlePath: location.Bundle, TrustedRootPath: location.TrustedRoot,
		},
		Offline: offline, Role: trust.RoleStatus, Policy: policy,
	})
}

func validateSignedDocumentLocation(label string, location signedDocumentLocation, offline bool) error {
	if strings.TrimSpace(location.Document) == "" {
		return e.New(e.CodeUsage, "%s document path is required", label)
	}
	return validateQualifiedMaterial(label, trust.SignatureMaterial{
		SignaturePath: location.Signature, CertificatePath: location.Certificate,
		BundlePath: location.Bundle, TrustedRootPath: location.TrustedRoot,
	}, offline)
}

func qualifiedStatusIdentities(c *cli.Context) ([]string, error) {
	identities := append([]string(nil), c.StringSlice("status-identity")...)
	if len(identities) == 0 {
		store, err := loadTrustStoreFile(c.Context, trustStorePath(c))
		if err != nil {
			return nil, err
		}
		identities = append(identities, store.StatusIdentities...)
		if store.StatusSigner != "" {
			identities = append(identities, store.StatusSigner)
		}
	}
	return normalizedRoleIdentities(identities, "status checkpoint")
}

func statusHighWaterFromReceipt(receipt status.CheckpointReceipt) (statusHighWaterFile, error) {
	target, err := receipt.Target()
	if err != nil {
		return statusHighWaterFile{}, err
	}
	authority, err := receipt.Authority()
	if err != nil {
		return statusHighWaterFile{}, err
	}
	sequence, err := receipt.Sequence()
	if err != nil {
		return statusHighWaterFile{}, err
	}
	digest, err := receipt.CheckpointDigest()
	if err != nil {
		return statusHighWaterFile{}, err
	}
	return statusHighWaterFile{SchemaVersion: statusHighWaterSchemaVersion, Target: target,
		Authority: authority, Sequence: sequence, CheckpointDigest: digest}, nil
}

func readCanonicalJSON(path string, destination any) error {
	return readCanonicalJSONContext(context.Background(), path, destination)
}

func readCanonicalJSONContext(ctx context.Context, path string, destination any) error {
	if err := statusContextError(ctx, "reading canonical status JSON"); err != nil {
		return err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "reading %s", path)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return e.New(e.CodeInternal, "holding canonical status JSON %s", path)
	}
	defer func() { _ = file.Close() }()
	before, err := file.Stat()
	if err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "stating %s", path)
	}
	pathInfo, pathErr := os.Lstat(path)
	if pathErr != nil || !before.Mode().IsRegular() || !pathInfo.Mode().IsRegular() ||
		!os.SameFile(before, pathInfo) || before.Size() < 0 || before.Size() > trust.MaxAuthenticatedDocumentBytes {
		return e.New(e.CodeInvalidCatalog, "%s must be a regular JSON document no larger than %d bytes", path, trust.MaxAuthenticatedDocumentBytes)
	}
	raw, err := io.ReadAll(io.LimitReader(statusContextReader{ctx: ctx, source: file}, trust.MaxAuthenticatedDocumentBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return e.Wrap(e.CodeTimeout, ctx.Err(), "reading canonical status JSON %s", path)
		}
		return e.Wrap(e.CodeInvalidCatalog, err, "reading %s", path)
	}
	if int64(len(raw)) > trust.MaxAuthenticatedDocumentBytes {
		return e.New(e.CodeInvalidCatalog, "%s exceeds the %d-byte JSON document limit", path, trust.MaxAuthenticatedDocumentBytes)
	}
	after, statErr := file.Stat()
	afterPath, afterPathErr := os.Lstat(path)
	if statErr != nil || afterPathErr != nil || !os.SameFile(before, after) || !os.SameFile(before, afterPath) ||
		before.Size() != int64(len(raw)) || after.Size() != before.Size() || before.Mode() != after.Mode() ||
		!before.ModTime().Equal(after.ModTime()) {
		return e.New(e.CodeInvalidCatalog, "canonical status JSON %s changed while reading", path)
	}
	if err := statusContextError(ctx, "reading canonical status JSON"); err != nil {
		return err
	}
	if err := canonical.UnmarshalExact(raw, destination); err != nil {
		return e.Wrap(e.CodeInvalidCatalog, err, "reading canonical JSON %s", path)
	}
	return nil
}

func readProtectedHighWater(path string, destination *statusHighWaterFile) ([]byte, error) {
	return readProtectedHighWaterContext(context.Background(), path, destination)
}

func readProtectedHighWaterContext(ctx context.Context, path string, destination *statusHighWaterFile) ([]byte, error) {
	raw, err := readProtectedHighWaterBytesContext(ctx, path, e.CodeStatusCheckpoint)
	if err != nil {
		return nil, err
	}
	if err := canonical.UnmarshalExact(raw, destination); err != nil {
		return nil, e.Wrap(e.CodeStatusCheckpoint, err, "decoding protected status high-water %s", path)
	}
	return raw, nil
}

func readProtectedHighWaterBytesContext(ctx context.Context, path string, code e.Code) ([]byte, error) {
	if err := statusContextError(ctx, "reading protected status high-water"); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, e.Wrap(code, err, "opening protected status high-water %s without following links", path)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, e.New(code, "opening protected status high-water capability %s", path)
	}
	before, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, e.Wrap(code, err, "inspecting protected status high-water %s", path)
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0o077 != 0 || before.Size() > 1<<20 {
		_ = file.Close()
		return nil, e.New(code,
			"status high-water %s must be a regular owner-only file (mode 0600 or stricter) no larger than 1 MiB", path)
	}
	raw, readErr := io.ReadAll(io.LimitReader(statusContextReader{ctx: ctx, source: file}, (1<<20)+1))
	after, statErr := file.Stat()
	pathAfter, pathErr := os.Lstat(path)
	closeErr := file.Close()
	if readErr != nil {
		if ctx.Err() != nil {
			return nil, e.Wrap(e.CodeTimeout, ctx.Err(), "reading protected status high-water %s", path)
		}
		return nil, e.Wrap(code, readErr, "reading protected status high-water %s", path)
	}
	if statErr != nil {
		return nil, e.Wrap(code, statErr, "rechecking protected status high-water %s", path)
	}
	if pathErr != nil {
		return nil, e.Wrap(code, pathErr, "rechecking protected status high-water path %s", path)
	}
	if closeErr != nil {
		return nil, e.Wrap(code, closeErr, "closing protected status high-water %s", path)
	}
	if len(raw) > 1<<20 || int64(len(raw)) != before.Size() ||
		!after.Mode().IsRegular() || !pathAfter.Mode().IsRegular() ||
		!os.SameFile(before, after) || !os.SameFile(before, pathAfter) ||
		after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) ||
		after.Mode() != before.Mode() {
		return nil, e.New(code, "status high-water %s changed while reading", path)
	}
	if err := statusContextError(ctx, "reading protected status high-water"); err != nil {
		return nil, err
	}
	return raw, nil
}

func statusContextError(ctx context.Context, operation string) error {
	if ctx == nil {
		return e.New(e.CodeUsage, "%s requires a non-nil context", operation)
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "%s canceled", operation)
	}
	return nil
}

type statusContextReader struct {
	ctx    context.Context
	source io.Reader
}

func (reader statusContextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.source.Read(buffer)
}

// persistStatusHighWater is a cross-process compare-and-swap. A stale command can
// never overwrite a newer sequence: it must hold the kernel-released advisory lock
// and prove the exact protected bytes are the snapshot it evaluated before publishing
// the next state. The lock inode is intentionally persistent; process death releases
// flock without leaving an O_EXCL lease that requires unsafe stale-file guessing.
func persistStatusHighWater(path string, observed, next []byte) error {
	return persistStatusHighWaterContext(context.Background(), path, observed, next)
}

func persistStatusHighWaterContext(ctx context.Context, path string, observed, next []byte) error {
	if err := statusContextError(ctx, "persisting status high-water"); err != nil {
		return err
	}
	if len(observed) > 1<<20 || len(next) > 1<<20 {
		return e.New(e.CodeStatusCheckpoint, "status high-water exceeds its 1 MiB bound")
	}
	dir := filepath.Dir(path)
	temporary, err := os.CreateTemp(dir, ".nkd-status-high-water-*.tmp")
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "staging status high-water %s", path)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return e.Wrap(e.CodeInternal, err, "protecting staged status high-water")
	}
	if _, err := io.CopyBuffer(temporary, statusContextReader{ctx: ctx, source: bytes.NewReader(next)}, make([]byte, 128<<10)); err != nil {
		_ = temporary.Close()
		if ctx.Err() != nil {
			return e.Wrap(e.CodeTimeout, ctx.Err(), "writing staged status high-water")
		}
		return e.Wrap(e.CodeInternal, err, "writing staged status high-water")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return e.Wrap(e.CodeInternal, err, "syncing staged status high-water")
	}
	if err := statusContextError(ctx, "staging status high-water"); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return e.Wrap(e.CodeInternal, err, "closing staged status high-water")
	}

	lease, err := acquireStatusHighWaterLockContext(ctx, path+".lease")
	if err != nil {
		return err
	}
	defer func() {
		_ = unix.Flock(int(lease.Fd()), unix.LOCK_UN)
		_ = lease.Close()
	}()
	current, err := readProtectedHighWaterBytesContext(ctx, path, e.CodeWriteConflict)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, observed) {
		return e.New(e.CodeWriteConflict, "status high-water changed while checkpoint verification was in progress")
	}
	if err := statusContextError(ctx, "committing status high-water"); err != nil {
		return err
	}
	// Rename is the publication commit point. Once it succeeds, complete directory
	// durability and report the committed result even if cancellation arrives.
	if err := os.Rename(temporaryPath, path); err != nil {
		return e.Wrap(e.CodeInternal, err, "committing status high-water")
	}
	directory, err := os.Open(dir)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "opening status high-water directory")
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return e.Wrap(e.CodeInternal, syncErr, "syncing status high-water directory")
	}
	if closeErr != nil {
		return e.Wrap(e.CodeInternal, closeErr, "closing status high-water directory")
	}
	return nil
}

func acquireStatusHighWaterLockContext(ctx context.Context, path string) (*os.File, error) {
	if err := statusContextError(ctx, "acquiring status high-water lock"); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, e.Wrap(e.CodeWriteConflict, err, "opening status high-water advisory lock %s", path)
	}
	lease := os.NewFile(uintptr(fd), path)
	if lease == nil {
		_ = unix.Close(fd)
		return nil, e.New(e.CodeInternal, "opening status high-water advisory lock %s", path)
	}
	closeWith := func(err error) (*os.File, error) {
		_ = lease.Close()
		return nil, err
	}
	info, err := lease.Stat()
	if err != nil {
		return closeWith(e.Wrap(e.CodeWriteConflict, err, "inspecting status high-water advisory lock %s", path))
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return closeWith(e.New(e.CodeWriteConflict,
			"status high-water advisory lock %s must be a regular owner-only file", path))
	}
	for {
		if err := statusContextError(ctx, "acquiring status high-water lock"); err != nil {
			return closeWith(err)
		}
		err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return closeWith(e.New(e.CodeWriteConflict,
				"another status high-water writer holds advisory lock %s", path))
		}
		return closeWith(e.Wrap(e.CodeWriteConflict, err,
			"locking status high-water advisory lock %s", path))
	}
	if err := lease.Sync(); err != nil {
		_ = unix.Flock(fd, unix.LOCK_UN)
		return closeWith(e.Wrap(e.CodeInternal, err, "syncing status high-water advisory lock"))
	}
	if err := statusContextError(ctx, "acquiring status high-water lock"); err != nil {
		_ = unix.Flock(fd, unix.LOCK_UN)
		return closeWith(err)
	}
	// Persist the reusable lock inode itself. Subsequent crashes release only the
	// kernel lock; they never require deleting this directory entry.
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		_ = unix.Flock(fd, unix.LOCK_UN)
		return closeWith(e.Wrap(e.CodeInternal, err, "opening status high-water lock directory"))
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		_ = unix.Flock(fd, unix.LOCK_UN)
		return closeWith(e.Wrap(e.CodeInternal, syncErr, "syncing status high-water lock directory"))
	}
	if closeErr != nil {
		_ = unix.Flock(fd, unix.LOCK_UN)
		return closeWith(e.Wrap(e.CodeInternal, closeErr, "closing status high-water lock directory"))
	}
	return lease, nil
}
