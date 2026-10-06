package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// Source checkout and Git writes stay beneath opened directory capabilities.
// These handles confer filesystem access only, not build or evidence authority.
func holdSourceFetchDirectory(ctx context.Context, path string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "opening source fetch directory")
	}
	root, relative := ".", path
	if filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, e.Wrap(e.CodeFetchFailed, err, "locating source fetch workspace")
		}
		relative, err = filepath.Rel(cwd, path)
		if err != nil {
			return nil, e.Wrap(e.CodeFetchFailed, err, "locating source fetch directory")
		}
		if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			// Local callers can select an absolute workspace outside cwd. Hold its
			// nearest existing directory as the explicit root; generated Plan paths
			// instead stay beneath cwd and never use this branch.
			root, relative = path, ""
			for {
				info, statErr := os.Lstat(root)
				if statErr == nil {
					if !info.IsDir() {
						return nil, e.New(e.CodeFetchFailed, "source fetch workspace is not a real directory")
					}
					break
				}
				if !os.IsNotExist(statErr) {
					return nil, e.Wrap(e.CodeFetchFailed, statErr, "locating source fetch workspace")
				}
				relative = filepath.Join(filepath.Base(root), relative)
				root = filepath.Dir(root)
			}
		}
	}
	// CreateParent opens/creates every component with no-follow descriptor access;
	// the final placeholder is never created or used as a source member.
	directory, _, err := securepath.CreateParentBeneath(root, filepath.Join(relative, ".nkd-held-directory"))
	if err != nil {
		return nil, e.Wrap(e.CodeFetchFailed, err, "holding safe source fetch directory")
	}
	return directory, nil
}

func holdSourceFetchWorkspaceDirectory(ctx context.Context, workspace *os.File, relative string) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, e.Wrap(e.CodeTimeout, err, "opening source fetch directory")
	}
	if !securepath.WorkspaceRelative(relative) || filepath.Clean(relative) != relative {
		return nil, e.New(e.CodeEngineCapability, "held source fetching requires an exact workspace-relative path")
	}
	directory, _, err := securepath.CreateParent(workspace, filepath.Join(relative, ".nkd-held-directory"))
	if err != nil {
		return nil, e.Wrap(e.CodeFetchFailed, err, "holding safe source fetch directory")
	}
	return directory, nil
}

func sourceFetchDescriptorPath(fd uintptr) (string, error) {
	switch runtime.GOOS {
	case "linux":
		return fmt.Sprintf("/proc/self/fd/%d/.", fd), nil
	default:
		return "", e.New(e.CodeEngineCapability, "source fetch requires inherited directory descriptors")
	}
}

func sourceFetchGitOut(opts RunOptions, dir string, args ...string) (string, error) {
	if opts.fetchDirectory == nil {
		return gitOut(opts.ctx(), dir, args...)
	}
	path, err := sourceFetchDescriptorPath(3)
	if err != nil {
		return "", err
	}
	command := []string{"-C", path}
	files := []*os.File{opts.fetchDirectory}
	if opts.fetchGitDirectory != nil {
		if err := recheckSourceFetchGitDirectory(opts); err != nil {
			return "", err
		}
		// ExtraFiles starts at FD3: checkout is FD3 and Git metadata is FD4.
		gitPath, err := sourceFetchDescriptorPath(4)
		if err != nil {
			return "", err
		}
		command = append(command, "--git-dir="+gitPath, "--work-tree="+path,
			"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false")
		files = append(files, opts.fetchGitDirectory)
	}
	cmd := execCmd(opts, "git", append(command, args...)...)
	cmd.Env = append([]string(nil), opts.SourceGitEnvironment...)
	if cmd.Env == nil {
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=credential.helper", "GIT_CONFIG_VALUE_0="}
	}
	cmd.ExtraFiles = files
	out, err := outputOwnedCommand(opts.ctx(), cmd)
	if err != nil {
		if opts.ctx().Err() != nil {
			return "", e.Wrap(e.CodeTimeout, opts.ctx().Err(), "source Git execution canceled")
		}
		return "", err
	}
	if opts.fetchGitDirectory != nil {
		if err := recheckSourceFetchGitDirectory(opts); err != nil {
			return "", err
		}
	}
	return strings.TrimSpace(string(out)), nil
}

func recheckSourceFetchGitDirectory(opts RunOptions) error {
	current, info, err := securepath.OpenChild(opts.fetchDirectory, ".git", true)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "rechecking held source Git directory")
	}
	defer current.Close()
	held, err := opts.fetchGitDirectory.Stat()
	if err != nil || !os.SameFile(info, held) {
		return e.Wrap(e.CodeFetchFailed, err, "source Git directory changed after admission")
	}
	return nil
}

// Authenticate the selected logical outputs against the original directories,
// without creating components or following replacement ancestor links.
func recheckSourceFetchDirectories(node ir.Node, opts RunOptions) error {
	if opts.fetchWorkspace == nil {
		return nil // Ordinary Local and package-private IO fixtures own other paths.
	}
	receipt, err := sourceReceiptPath(opts.SourceRoot, node.Dest)
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "locating held source receipt")
	}
	for _, selected := range []struct {
		path string
		file *os.File
	}{
		{filepath.Join(opts.SourceRoot, node.Dest), opts.fetchDirectory},
		{filepath.Dir(receipt), opts.fetchReceiptDirectory},
	} {
		if err := recheckSourceFetchDirectory(opts.ctx(), opts.fetchWorkspace, selected.path, selected.file); err != nil {
			return err
		}
	}
	return nil
}

func recheckSourceFetchDirectory(ctx context.Context, workspace *os.File, relative string, held *os.File) error {
	if held == nil || !securepath.WorkspaceRelative(relative) || filepath.Clean(relative) != relative {
		return e.New(e.CodeFetchFailed, "source fetch output lost its held directory binding")
	}
	current, owned := workspace, false
	defer func() {
		if owned {
			_ = current.Close()
		}
	}()
	for _, component := range strings.Split(filepath.ToSlash(relative), "/") {
		if err := ctx.Err(); err != nil {
			return e.Wrap(e.CodeTimeout, err, "source directory recheck canceled")
		}
		child, _, err := securepath.OpenChild(current, component, true)
		if err != nil {
			return e.Wrap(e.CodeFetchFailed, err, "source fetch directory changed during execution")
		}
		if owned {
			_ = current.Close()
		}
		current, owned = child, true
	}
	actual, err := current.Stat()
	if err != nil {
		return e.Wrap(e.CodeFetchFailed, err, "rechecking source fetch directory")
	}
	original, err := held.Stat()
	if err != nil || !os.SameFile(actual, original) {
		return e.Wrap(e.CodeFetchFailed, err, "source fetch directory no longer matches its original owner")
	}
	return nil
}
