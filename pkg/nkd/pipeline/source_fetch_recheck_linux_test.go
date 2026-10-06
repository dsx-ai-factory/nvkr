//go:build linux

package pipeline

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/NVIDIA/nke-recipes/internal/securepath"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
)

// Replace real directories, not just symlinks, at each distinct receipt boundary.
// The held checkout/receipt descriptors must never authorize their replacements.
func TestSourceFetchRechecksLogicalDirectoriesAtReceiptBoundaries(t *testing.T) {
	for _, stage := range []string{"before receipt read", "receipt reuse", "before receipt commit"} {
		for _, selected := range []string{"checkout", "receipts"} {
			t.Run(stage+"/"+selected, func(t *testing.T) {
				node, checkoutPath, opts := pristineClosureFixture(t)
				node.VendorCmd = []string{"modeled-vendor"}
				node.VendorImage = "registry.example/vendor@sha256:" + strings.Repeat("1", 64)
				t.Chdir(filepath.Dir(opts.SourceRoot))
				opts.SourceRoot = "src"
				workspace, err := HoldSourceWorkspace(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = workspace.Close() })
				call, root, release, err := workspace.borrow(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(release)
				checkout, _, err := securepath.OpenDirectory(checkoutPath)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = checkout.Close() })
				receiptPath, err := sourceReceiptPath(opts.SourceRoot, node.Dest)
				if err != nil {
					t.Fatal(err)
				}
				receipts, err := holdSourceFetchWorkspaceDirectory(call, root, filepath.Dir(receiptPath))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = receipts.Close() })
				opts.Ctx, opts.SourceWorkspace, opts.fetchWorkspace = call, workspace, root
				opts.fetchDirectory, opts.fetchReceiptDirectory = checkout, receipts
				path := checkoutPath
				if selected == "receipts" {
					path = filepath.Dir(receiptPath)
				}
				swap := func() error {
					if err := os.Rename(path, path+".original"); err != nil {
						return err
					}
					return os.Mkdir(path, 0o700)
				}
				var mutation *vendorCopyMutationContext
				switch stage {
				case "receipt reuse":
					vendor, err := fetchVendorSpecDigest(node)
					if err != nil {
						t.Fatal(err)
					}
					closure, err := sourceTreeDigestContext(call, checkoutPath)
					if err != nil {
						t.Fatal(err)
					}
					if err := writeSourceReceiptContext(call, receiptPath, sourceReceipt{
						Schema: sourceReceiptSchema, Commit: node.Commit, TreeHash: node.TreeHash,
						VendorSpecDigest: vendor, ClosureDigest: closure,
					}, receipts); err != nil {
						t.Fatal(err)
					}
					mutation = &vendorCopyMutationContext{Context: call}
					mutation.mutate = func() (bool, error) {
						var pcs [24]uintptr
						frames := runtime.CallersFrames(pcs[:runtime.Callers(2, pcs[:])])
						for {
							frame, more := frames.Next()
							if strings.HasSuffix(frame.Function, ".sourceTreeManifestContext") {
								return true, swap()
							}
							if !more {
								return false, nil
							}
						}
					}
					opts.Ctx = mutation
				case "before receipt read":
					if err := swap(); err != nil {
						t.Fatal(err)
					}
				}
				calls := 0
				err = ensureSourceClosure(node, checkoutPath, opts, func() error {
					calls++
					if stage != "before receipt commit" {
						t.Fatal("rejected or receipted source reached vendor")
					}
					return swap()
				})
				if code(err) != e.CodeFetchFailed || !strings.Contains(err.Error(), "original owner") {
					t.Fatalf("replacement did not reach original-directory guard: %v", err)
				}
				if mutation != nil && (!mutation.hit || mutation.err != nil) {
					t.Fatalf("receipt-reuse mutation not reached: hit=%t err=%v", mutation.hit, mutation.err)
				}
				wantCalls := 0
				if stage == "before receipt commit" {
					wantCalls = 1
				}
				if calls != wantCalls {
					t.Fatalf("vendor calls: got %d want %d", calls, wantCalls)
				}
				if stage != "receipt reuse" {
					_, exists, err := readSourceReceiptContext(call, receiptPath, receipts)
					if err != nil || exists {
						t.Fatalf("replacement issued a receipt: exists=%t err=%v", exists, err)
					}
				}
				entries, err := os.ReadDir(path)
				if err != nil || len(entries) != 0 {
					t.Fatalf("replacement received writes: %v / %v", entries, err)
				}
			})
		}
	}
}
