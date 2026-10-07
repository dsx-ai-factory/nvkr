// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/assemble"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/ir"
)

// verify is the unconditional terminal node. It is authoritative about the current
// run: every included build must have produced an artifact THIS run (present in
// collected), and each artifact is re-opened and re-hashed and must match the
// digest recorded at build time. A missing, unreadable, or changed artifact fails
// closed — a stale artifact left by a previous run cannot satisfy it, because a
// build that failed this run contributes no collected entry (NKD-FR-BLD-006).
func (l Local) verify(n ir.Node, producers map[string]ir.Node, included map[string]bool, collected []ArtifactResult, opts RunOptions) error {
	if opts.DryRun {
		fmt.Fprintf(opts.Stdout, "[verify] would check %d artifact(s)\n", countIncludedProducers(producers, included))
		return nil
	}
	recorded := make(map[string]ArtifactResult, len(collected))
	for _, a := range collected {
		recorded[a.Component] = a
	}
	var comps []string
	producerByComponent := make(map[string]ir.Node, len(producers))
	for id, pn := range producers {
		if included[id] {
			comps = append(comps, pn.Component)
			producerByComponent[pn.Component] = pn
		}
	}
	sort.Strings(comps)
	var problems []string
	for _, comp := range comps {
		a, ok := recorded[comp]
		if !ok {
			problems = append(problems, comp+": no artifact produced this run")
			continue
		}
		switch {
		case a.Ref != "":
			// A pulled, content-addressed OCI image (no-compile lane): verify the
			// complete transported layout closure offline. No daemon/socket is trusted.
			if _, err := captureRegularTreeContext(opts.ctx(), a.TransportPath); err != nil {
				problems = append(problems, comp+": unsafe OCI artifact tree ("+err.Error()+")")
				continue
			}
			if err := opts.OCI.Verify(opts.ctx(), a.Ref, a.TransportPath); err != nil {
				problems = append(problems, comp+": "+err.Error())
			}
		case a.Packaged && a.OutputType == "oci-image":
			// A from-source assembled OCI layout: re-check every blob (content hash ==
			// filename) so layer/config corruption is caught, not just the manifest blob.
			if _, err := captureRegularTreeContext(opts.ctx(), a.Path); err != nil {
				problems = append(problems, comp+": unsafe OCI artifact tree ("+err.Error()+")")
				continue
			}
			if err := assemble.VerifyLayoutContext(opts.ctx(), a.Path); err != nil {
				problems = append(problems, comp+": "+err.Error())
			}
		default:
			// A binary or .deb (built or acquired): re-hash the single file.
			root, expectedPath, pathErr := trustedArtifactPath(producerByComponent[comp], a, opts)
			if pathErr != nil || a.AbsPath != expectedPath {
				problems = append(problems, comp+": artifact path is not graph-derived")
				continue
			}
			relative, pathErr := relativeBeneath(root, expectedPath)
			if pathErr != nil {
				problems = append(problems, comp+": unsafe artifact path ("+pathErr.Error()+")")
				continue
			}
			sum, _, _, err := hashRegularBeneathContext(opts.ctx(), root, relative)
			if err != nil {
				problems = append(problems, comp+": unreadable ("+err.Error()+")")
				continue
			}
			if sum != a.SHA256 {
				problems = append(problems, comp+": digest changed since acquisition ("+a.SHA256+" != "+sum+")")
			}
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return e.New(e.CodeVerifyFailed, "verify failed: %v", problems)
	}
	return nil
}

func countIncludedProducers(producers map[string]ir.Node, included map[string]bool) int {
	n := 0
	for id := range producers {
		if included[id] {
			n++
		}
	}
	return n
}

func sha256File(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), size, nil
}
