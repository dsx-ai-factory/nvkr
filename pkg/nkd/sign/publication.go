package sign

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

const (
	publicationJournalSchema = "nkd.signed-publication-pending/v1"
	maxPublicationJournal    = 1 << 20
)

// PublicationRequest names the immutable detached-signature set to publish.
// DocumentPath owns the document, DocumentPath+".sig" owns the signature, and
// DocumentPath+".pem" owns the certificate. BundlePath is required exactly when
// the signing operation captured a Sigstore bundle.
type PublicationRequest struct {
	Document     []byte
	DocumentPath string
	BundlePath   string
}

type publicationMember struct {
	target    string
	data      []byte
	stage     string
	stageInfo os.FileInfo
	committed bool
}

type publicationJournal struct {
	SchemaVersion  string                     `json:"schemaVersion"`
	DocumentDigest string                     `json:"documentDigest"`
	Members        []publicationJournalMember `json:"members"`
}

type publicationJournalMember struct {
	Target string `json:"target"`
	Stage  string `json:"stage"`
	SHA256 string `json:"sha256"`
}

type publicationHook func(committed int, target string) error

type publicationContextReader struct {
	ctx    context.Context
	source io.Reader
}

func (reader publicationContextReader) Read(buffer []byte) (int, error) {
	if err := contextError(reader.ctx); err != nil {
		return 0, err
	}
	return reader.source.Read(buffer)
}

// Publish commits this exact signed-document set with no-overwrite semantics.
// Every member is staged and fsynced, then a durable recovery journal is linked
// into place before the first public member. A returned failure rolls back every
// member created by this call. A crash leaves the journal and exact hard-linked
// staging inodes, so a retry can either finalize a complete set or remove only
// that transaction's partial generation before proceeding.
func (s SignedDocument) Publish(ctx context.Context, request PublicationRequest) error {
	return s.publish(ctx, request, nil)
}

// PreparePublication validates the destination set and recovers an interrupted
// transaction for the same exact document before a new signing operation begins.
// Recovery is intentionally independent of signature/certificate bytes: keyless
// re-signing is not byte-deterministic, so requiring a retry to recreate old
// sidecars would make a real process crash permanently unrecoverable.
func PreparePublication(ctx context.Context, request PublicationRequest) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	targets, err := publicationTargets(ctx, request)
	if err != nil {
		return err
	}
	members := make([]publicationMember, len(targets))
	for i := range targets {
		members[i].target = targets[i]
	}
	journalPath := members[0].target + ".publish.pending.json"
	lock, err := acquirePublicationLock(ctx, members[0].target+".publish.lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := recoverPublicationForRetry(ctx, journalPath, request.Document, members); err != nil {
		return err
	}
	return rejectExistingPublicationMembers(members)
}

func (s SignedDocument) publish(ctx context.Context, request PublicationRequest, hook publicationHook) (resultErr error) {
	members, err := s.publicationMembers(ctx, request)
	if err != nil {
		return err
	}
	journalPath := members[0].target + ".publish.pending.json"
	lock, err := acquirePublicationLock(ctx, members[0].target+".publish.lock")
	if err != nil {
		return err
	}
	defer lock.Close()

	if err := recoverPublicationForRetry(ctx, journalPath, request.Document, members); err != nil {
		return err
	}
	if err := rejectExistingPublicationMembers(members); err != nil {
		return err
	}

	directories := publicationDirectories(members)
	journalInstalled := false
	defer func() {
		if resultErr == nil {
			return
		}
		rollbackErr := rollbackPublication(members, journalPath, journalInstalled, directories)
		if rollbackErr != nil {
			resultErr = e.Wrap(e.CodeInternal, errors.Join(resultErr, rollbackErr),
				"signed-document publication rollback was incomplete")
		}
	}()

	for i := range members {
		if err := contextError(ctx); err != nil {
			return err
		}
		stage, err := os.CreateTemp(filepath.Dir(members[i].target), ".nkd-signed-*.tmp")
		if err != nil {
			return e.Wrap(e.CodeInternal, err,
				"staging signed-document member %s", members[i].target)
		}
		members[i].stage = stage.Name()
		if err := stage.Chmod(0o644); err != nil {
			_ = stage.Close()
			return e.Wrap(e.CodeInternal, err,
				"setting signed-document member mode for %s", members[i].target)
		}
		if _, err := io.CopyBuffer(stage,
			publicationContextReader{ctx: ctx, source: bytes.NewReader(members[i].data)},
			make([]byte, 128<<10)); err != nil {
			_ = stage.Close()
			if ctxErr := contextError(ctx); ctxErr != nil {
				return ctxErr
			}
			return e.Wrap(e.CodeInternal, err,
				"writing signed-document member %s", members[i].target)
		}
		if err := stage.Sync(); err != nil {
			_ = stage.Close()
			return e.Wrap(e.CodeInternal, err,
				"syncing signed-document member %s", members[i].target)
		}
		if err := stage.Close(); err != nil {
			return e.Wrap(e.CodeInternal, err,
				"closing signed-document member %s", members[i].target)
		}
		members[i].stageInfo, err = os.Lstat(members[i].stage)
		if err != nil {
			return e.Wrap(e.CodeInternal, err,
				"reopening staged signed-document member %s", members[i].target)
		}
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := installPublicationJournal(ctx, journalPath, members); err != nil {
		return err
	}
	journalInstalled = true

	for i := range members {
		if err := contextError(ctx); err != nil {
			return err
		}
		if err := os.Link(members[i].stage, members[i].target); err != nil {
			if errors.Is(err, os.ErrExist) {
				return e.Wrap(e.CodeWriteConflict, err,
					"signed-document member %s appeared during immutable commit", members[i].target)
			}
			return e.Wrap(e.CodeInternal, err,
				"committing signed-document member %s", members[i].target)
		}
		members[i].committed = true
		targetInfo, targetErr := os.Lstat(members[i].target)
		if targetErr != nil || !os.SameFile(members[i].stageInfo, targetInfo) {
			return e.New(e.CodeWriteConflict,
				"signed-document member %s changed during immutable commit", members[i].target)
		}
		if hook != nil {
			if err := hook(i+1, members[i].target); err != nil {
				return e.Wrap(e.CodeInternal, err,
					"publishing signed-document member %s", members[i].target)
			}
		}
		if err := contextError(ctx); err != nil {
			return err
		}
	}
	if err := syncPublicationDirectories(directories); err != nil {
		return err
	}
	if err := os.Remove(journalPath); err != nil {
		return e.Wrap(e.CodeInternal, err,
			"committing signed-document publication journal %s", journalPath)
	}
	journalInstalled = false
	if err := syncPublicationDirectories(map[string]struct{}{filepath.Dir(journalPath): {}}); err != nil {
		return err
	}
	if err := removePublicationStages(members); err != nil {
		return err
	}
	return syncPublicationDirectories(directories)
}

func (s SignedDocument) publicationMembers(ctx context.Context, request PublicationRequest) ([]publicationMember, error) {
	if err := s.RequireFor(ctx, request.Document); err != nil {
		return nil, err
	}
	signature, err := s.Signature()
	if err != nil {
		return nil, err
	}
	certificate, err := s.Certificate()
	if err != nil {
		return nil, err
	}
	bundle, err := s.Bundle()
	if err != nil {
		return nil, err
	}
	if len(bundle) == 0 && request.BundlePath != "" {
		return nil, e.New(e.CodeUsage,
			"bundle output path was supplied for a signing operation without a bundle")
	}
	if len(bundle) != 0 && request.BundlePath == "" {
		return nil, e.New(e.CodeUsage,
			"qualified signing captured a bundle but publication omitted its output path")
	}
	targets, err := publicationTargets(ctx, request)
	if err != nil {
		return nil, err
	}
	data := [][]byte{append([]byte(nil), request.Document...), signature, certificate}
	if request.BundlePath != "" {
		data = append(data, bundle)
	}
	members := make([]publicationMember, len(targets))
	for i := range targets {
		members[i] = publicationMember{target: targets[i], data: data[i]}
	}
	return members, nil
}

func publicationTargets(ctx context.Context, request PublicationRequest) ([]string, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if request.DocumentPath == "" || filepath.Clean(request.DocumentPath) == "." {
		return nil, e.New(e.CodeUsage, "signed-document publication requires an output path")
	}
	documentPath, err := filepath.Abs(filepath.Clean(request.DocumentPath))
	if err != nil {
		return nil, e.Wrap(e.CodeUsage, err,
			"resolving signed-document output %s", request.DocumentPath)
	}
	targets := []string{documentPath, documentPath + ".sig", documentPath + ".pem"}
	if request.BundlePath != "" {
		bundlePath, err := filepath.Abs(filepath.Clean(request.BundlePath))
		if err != nil {
			return nil, e.Wrap(e.CodeUsage, err,
				"resolving signed-document bundle output %s", request.BundlePath)
		}
		targets = append(targets, bundlePath)
	}
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		if _, duplicate := seen[target]; duplicate {
			return nil, e.New(e.CodeUsage,
				"signed-document outputs resolve to the same path %s", target)
		}
		seen[target] = struct{}{}
		parent := filepath.Dir(target)
		info, err := os.Stat(parent)
		if err != nil {
			return nil, e.Wrap(e.CodeInternal, err,
				"opening signed-document publication directory %s", parent)
		}
		if !info.IsDir() {
			return nil, e.New(e.CodeUsage,
				"signed-document publication parent %s is not a directory", parent)
		}
	}
	return targets, nil
}

func installPublicationJournal(ctx context.Context, path string, members []publicationMember) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	journal := publicationJournal{
		SchemaVersion:  publicationJournalSchema,
		DocumentDigest: digestBytes(members[0].data),
		Members:        make([]publicationJournalMember, len(members)),
	}
	for i, member := range members {
		journal.Members[i] = publicationJournalMember{
			Target: member.target, Stage: member.stage, SHA256: digestBytes(member.data),
		}
	}
	raw, err := json.Marshal(journal)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "encoding signed-document publication journal")
	}
	raw = append(raw, '\n')
	stage, err := os.CreateTemp(filepath.Dir(path), ".nkd-signed-journal-*.tmp")
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "staging signed-document publication journal")
	}
	stagePath := stage.Name()
	defer func() { _ = os.Remove(stagePath) }()
	if err := stage.Chmod(0o600); err != nil {
		_ = stage.Close()
		return e.Wrap(e.CodeInternal, err, "setting signed-document journal mode")
	}
	if _, err := io.CopyBuffer(stage,
		publicationContextReader{ctx: ctx, source: bytes.NewReader(raw)},
		make([]byte, 32<<10)); err != nil {
		_ = stage.Close()
		if ctxErr := contextError(ctx); ctxErr != nil {
			return ctxErr
		}
		return e.Wrap(e.CodeInternal, err, "writing signed-document publication journal")
	}
	if err := stage.Sync(); err != nil {
		_ = stage.Close()
		return e.Wrap(e.CodeInternal, err, "syncing signed-document publication journal")
	}
	if err := stage.Close(); err != nil {
		return e.Wrap(e.CodeInternal, err, "closing signed-document publication journal")
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := os.Link(stagePath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return e.Wrap(e.CodeWriteConflict, err,
				"signed-document publication journal %s already exists", path)
		}
		return e.Wrap(e.CodeInternal, err,
			"committing signed-document publication journal %s", path)
	}
	return syncPublicationDirectories(publicationDirectories(members))
}

func recoverPublicationForRetry(ctx context.Context, journalPath string, document []byte,
	members []publicationMember,
) error {
	journal, err := readPublicationJournal(ctx, journalPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := validatePendingPublicationRequest(journal, document, members); err != nil {
		return err
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	for i := range members {
		members[i].stage = journal.Members[i].Stage
		stageInfo, stageErr := os.Lstat(members[i].stage)
		targetInfo, targetErr := os.Lstat(members[i].target)
		if stageErr != nil {
			if errors.Is(stageErr, os.ErrNotExist) && errors.Is(targetErr, os.ErrNotExist) {
				continue
			}
			return e.Wrap(e.CodeWriteConflict, stageErr,
				"signed-document recovery stage %s is unavailable", members[i].stage)
		}
		members[i].stageInfo = stageInfo
		if !stageInfo.Mode().IsRegular() {
			return e.New(e.CodeWriteConflict,
				"signed-document recovery stage %s is not regular", members[i].stage)
		}
		if err := contextError(ctx); err != nil {
			return err
		}
		stageDigest, err := digestRegularFile(ctx, members[i].stage)
		if err != nil || stageDigest != journal.Members[i].SHA256 {
			return e.New(e.CodeWriteConflict,
				"signed-document recovery stage %s changed", members[i].stage)
		}
		switch {
		case targetErr == nil && os.SameFile(stageInfo, targetInfo):
			members[i].committed = true
		case errors.Is(targetErr, os.ErrNotExist):
		default:
			return e.New(e.CodeWriteConflict,
				"signed-document target %s does not belong to its pending transaction", members[i].target)
		}
	}
	directories := publicationDirectories(members)
	if err := rollbackPublication(members, journalPath, true, directories); err != nil {
		return err
	}
	return nil
}

func readPublicationJournal(ctx context.Context, path string) (publicationJournal, error) {
	if err := contextError(ctx); err != nil {
		return publicationJournal{}, err
	}
	file, held, err := openPublicationRegular(path)
	if err != nil {
		return publicationJournal{}, e.Wrap(e.CodeWriteConflict, err,
			"opening signed-document publication journal %s", path)
	}
	defer func() { _ = file.Close() }()
	if held.Mode().Perm()&0o077 != 0 {
		return publicationJournal{}, e.New(e.CodeWriteConflict,
			"signed-document publication journal %s is not an owner-only regular file", path)
	}
	if held.Size() > maxPublicationJournal {
		return publicationJournal{}, e.New(e.CodeWriteConflict,
			"signed-document publication journal %s is unreadable or oversized", path)
	}
	raw := make([]byte, 0, int(held.Size()))
	buffer := make([]byte, 32<<10)
	for {
		if err := contextError(ctx); err != nil {
			return publicationJournal{}, err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			if len(raw)+n > maxPublicationJournal {
				return publicationJournal{}, e.New(e.CodeWriteConflict,
					"signed-document publication journal %s is unreadable or oversized", path)
			}
			raw = append(raw, buffer[:n]...)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return publicationJournal{}, e.Wrap(e.CodeWriteConflict, readErr,
				"reading signed-document publication journal %s", path)
		}
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(held, after) {
		return publicationJournal{}, e.New(e.CodeWriteConflict,
			"signed-document publication journal %s changed while reading", path)
	}
	var journal publicationJournal
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&journal); err != nil {
		return publicationJournal{}, e.Wrap(e.CodeWriteConflict, err,
			"decoding signed-document publication journal %s", path)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return publicationJournal{}, e.Wrap(e.CodeWriteConflict, err,
			"decoding signed-document publication journal %s", path)
	}
	return journal, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON values")
	}
	return err
}

func validatePendingPublicationRequest(journal publicationJournal, document []byte,
	members []publicationMember,
) error {
	if journal.SchemaVersion != publicationJournalSchema ||
		journal.DocumentDigest != digestBytes(document) || len(journal.Members) != len(members) {
		return e.New(e.CodeWriteConflict,
			"pending signed-document publication does not match this exact document and output set")
	}
	for i := range members {
		entry := journal.Members[i]
		if entry.Target != members[i].target || filepath.Dir(entry.Stage) != filepath.Dir(entry.Target) ||
			!strings.HasPrefix(filepath.Base(entry.Stage), ".nkd-signed-") ||
			!strings.HasPrefix(entry.SHA256, "sha256:") || len(entry.SHA256) != len("sha256:")+64 {
			return e.New(e.CodeWriteConflict,
				"pending signed-document publication member %d does not match this request", i)
		}
	}
	return nil
}

func rejectExistingPublicationMembers(members []publicationMember) error {
	for _, member := range members {
		if _, err := os.Lstat(member.target); err == nil {
			return e.New(e.CodeImmutableViolation,
				"refusing to overwrite immutable signed-document member %s", member.target)
		} else if !errors.Is(err, os.ErrNotExist) {
			return e.Wrap(e.CodeInternal, err,
				"inspecting signed-document member %s", member.target)
		}
	}
	return nil
}

func rollbackPublication(members []publicationMember, journalPath string, journalInstalled bool,
	directories map[string]struct{},
) error {
	var rollbackErr error
	for i := len(members) - 1; i >= 0; i-- {
		if !members[i].committed {
			continue
		}
		targetInfo, targetErr := os.Lstat(members[i].target)
		switch {
		case members[i].stageInfo == nil:
			rollbackErr = errors.Join(rollbackErr,
				fmt.Errorf("staging inode for %s is unavailable", members[i].target))
		case targetErr == nil && os.SameFile(members[i].stageInfo, targetInfo):
			if err := os.Remove(members[i].target); err != nil {
				rollbackErr = errors.Join(rollbackErr,
					fmt.Errorf("removing committed member %s: %w", members[i].target, err))
			}
		case errors.Is(targetErr, os.ErrNotExist):
		default:
			rollbackErr = errors.Join(rollbackErr,
				fmt.Errorf("refusing to remove replaced member %s", members[i].target))
		}
	}
	if err := syncPublicationDirectories(directories); err != nil {
		rollbackErr = errors.Join(rollbackErr, err)
	}
	if err := removePublicationStages(members); err != nil {
		rollbackErr = errors.Join(rollbackErr, err)
	}
	if journalInstalled {
		if err := os.Remove(journalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErr = errors.Join(rollbackErr,
				fmt.Errorf("removing publication journal %s: %w", journalPath, err))
		}
	}
	if err := syncPublicationDirectories(directories); err != nil {
		rollbackErr = errors.Join(rollbackErr, err)
	}
	return rollbackErr
}

func removePublicationStages(members []publicationMember) error {
	var removeErr error
	for _, member := range members {
		if member.stage == "" {
			continue
		}
		if err := os.Remove(member.stage); err != nil && !errors.Is(err, os.ErrNotExist) {
			removeErr = errors.Join(removeErr,
				fmt.Errorf("removing staged member %s: %w", member.stage, err))
		}
	}
	if removeErr != nil {
		return e.Wrap(e.CodeInternal, removeErr,
			"removing signed-document publication stages")
	}
	return nil
}

func publicationDirectories(members []publicationMember) map[string]struct{} {
	directories := make(map[string]struct{}, len(members))
	for _, member := range members {
		directories[filepath.Dir(member.target)] = struct{}{}
	}
	return directories
}

func syncPublicationDirectories(directories map[string]struct{}) error {
	ordered := make([]string, 0, len(directories))
	for dir := range directories {
		ordered = append(ordered, dir)
	}
	sort.Strings(ordered)
	for _, dir := range ordered {
		handle, err := os.Open(dir)
		if err != nil {
			return e.Wrap(e.CodeInternal, err,
				"opening signed-document publication directory %s for sync", dir)
		}
		syncErr := handle.Sync()
		closeErr := handle.Close()
		if syncErr != nil {
			return e.Wrap(e.CodeInternal, syncErr,
				"syncing signed-document publication directory %s", dir)
		}
		if closeErr != nil {
			return e.Wrap(e.CodeInternal, closeErr,
				"closing signed-document publication directory %s", dir)
		}
	}
	return nil
}

func digestRegularFile(ctx context.Context, path string) (string, error) {
	if err := contextError(ctx); err != nil {
		return "", err
	}
	file, held, err := openPublicationRegular(path)
	if err != nil {
		return "", fmt.Errorf("opening regular file %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	if held.Size() > maxSignedDocumentBytes {
		return "", fmt.Errorf("regular file %s exceeds the signed-material size limit", path)
	}
	hash := sha256.New()
	buffer := make([]byte, 128<<10)
	var total int64
	for {
		if err := contextError(ctx); err != nil {
			return "", err
		}
		n, readErr := file.Read(buffer)
		if n > 0 {
			total += int64(n)
			if total > maxSignedDocumentBytes {
				return "", fmt.Errorf("regular file %s grew beyond the signed-material size limit", path)
			}
			_, _ = hash.Write(buffer[:n])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(held, after) || after.Size() != total {
		return "", fmt.Errorf("file %s changed while hashing", path)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
