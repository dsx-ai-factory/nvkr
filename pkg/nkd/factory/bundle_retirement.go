package factory

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/canonical"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

// A prior manifest is a bounded ownership description, not execution authority.
// It can retire only matching regular files inside this generator's directory.
func retiredGeneratedMembers(ctx context.Context, root string, bundle *GeneratedBundle) ([]GeneratedMember, error) {
	manifestPath, err := generatedDestination(root, bundle.ManifestPath)
	if err != nil {
		return nil, err
	}
	union := *bundle
	union.Files = make(map[string][]byte, len(bundle.Files))
	for name, raw := range bundle.Files {
		union.Files[name] = raw
	}
	var retired []GeneratedMember
	_, statErr := os.Lstat(manifestPath)
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, e.Wrap(e.CodePipelineDrift, statErr, "reading prior generated Run manifest")
	}
	if statErr == nil {
		raw, err := readGeneratedContext(ctx, manifestPath)
		if err != nil {
			return nil, e.Wrap(e.CodePipelineDrift, err, "reading prior generated Run manifest")
		}
		var prior GeneratedManifest
		if err := canonical.UnmarshalExact(bytes.TrimSuffix(raw, []byte{'\n'}), &prior); err != nil {
			return nil, e.Wrap(e.CodePipelineDrift, err, "decoding prior generated Run manifest")
		}
		if prior.SchemaVersion != generatedManifestSchema || prior.Engine != bundle.Engine || prior.WorkflowPath != bundle.WorkflowPath || len(prior.Members) > maxGeneratedDirEntries {
			return nil, e.New(e.CodePipelineDrift, "prior generated Run manifest has an unsupported layout")
		}
		prefix := filepath.ToSlash(filepath.Dir(bundle.ManifestPath)) + "/"
		previous := ""
		for _, member := range prior.Members {
			if !safeGeneratedPath(member.Path) || member.Path <= previous || member.Mode != 0o644 || !canonical.IsDigest(member.SHA256) || member.Path == bundle.ManifestPath || member.Path == bundle.ManifestPath+".lease" || (member.Path != bundle.WorkflowPath && !strings.HasPrefix(member.Path, prefix)) {
				return nil, e.New(e.CodePipelineDrift, "prior generated Run manifest has an invalid member %q", member.Path)
			}
			previous = member.Path
			union.Files[member.Path] = nil
			if _, retained := bundle.Files[member.Path]; !retained {
				retired = append(retired, member)
			}
		}
	}
	if err := checkGeneratedDirectoryContents(ctx, root, &union); err != nil {
		return nil, err
	}
	if err := checkRetiredGeneratedMembers(ctx, root, retired); err != nil {
		return nil, err
	}
	return retired, nil
}

func checkRetiredGeneratedMembers(ctx context.Context, root string, retired []GeneratedMember) error {
	for _, member := range retired {
		path, err := generatedDestination(root, member.Path)
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue // An interrupted prior retirement is safe to retry.
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != os.FileMode(member.Mode) {
			return e.New(e.CodePipelineDrift, "obsolete generated Run member %s is unsafe or modified", member.Path)
		}
		if err := validateGeneratedDirectoryChain(root, filepath.ToSlash(filepath.Dir(member.Path))); err != nil {
			return e.Wrap(e.CodePipelineDrift, err, "obsolete generated Run member has unsafe parents")
		}
		raw, err := readGeneratedContext(ctx, path)
		if err != nil || pipeline.FileDigest(raw) != member.SHA256 {
			return e.New(e.CodePipelineDrift, "obsolete generated Run member %s differs from its prior manifest", member.Path)
		}
		after, err := os.Lstat(path)
		if err != nil || !os.SameFile(info, after) {
			return e.New(e.CodePipelineDrift, "obsolete generated Run member %s changed during inspection", member.Path)
		}
	}
	return nil
}

func removeRetiredGeneratedMembers(ctx context.Context, root string, bundle *GeneratedBundle, retired []GeneratedMember) error {
	// The commit lock excludes other generator writers. A same-user external
	// editor is outside that lock; recheck exact bytes immediately before removal.
	if err := checkRetiredGeneratedMembers(ctx, root, retired); err != nil {
		return err
	}
	for _, member := range retired {
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(member.Path))); err != nil && !errors.Is(err, os.ErrNotExist) {
			return e.Wrap(e.CodePipelineDrift, err, "retiring generated Run member %s", member.Path)
		}
	}
	kept := map[string]bool{}
	for name := range bundle.Files {
		for parent := filepath.Dir(name); parent != "."; parent = filepath.Dir(parent) {
			kept[parent] = true
		}
	}
	owned := filepath.Dir(bundle.ManifestPath)
	candidates := map[string]bool{}
	for _, member := range retired {
		for parent := filepath.Dir(member.Path); parent != owned && strings.HasPrefix(parent, owned+string(filepath.Separator)); parent = filepath.Dir(parent) {
			if !kept[parent] {
				candidates[parent] = true
			}
		}
	}
	directories := sortedKeys(candidates)
	sort.Slice(directories, func(i, j int) bool { return len(directories[i]) > len(directories[j]) })
	for _, directory := range directories {
		// Remove is deliberately nonrecursive: an unexpected member prevents removal.
		if err := os.Remove(filepath.Join(root, directory)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return e.Wrap(e.CodePipelineDrift, err, "retiring generated Run directory %s", directory)
		}
	}
	return nil
}
