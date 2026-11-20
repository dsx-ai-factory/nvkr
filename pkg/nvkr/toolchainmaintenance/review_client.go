// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package toolchainmaintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/ownedcmd"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

const maxReviewBytes = 8 << 20

type reviewClient struct {
	root   string
	invoke func(context.Context, []string, []byte) ([]byte, error)
}

func newReviewClient(root string) reviewClient {
	c := reviewClient{root: root}
	c.invoke = func(ctx context.Context, args []string, input []byte) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if args[0] == "git" {
			return command(ctx, root, "git", args[1:]...)
		}
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Env = append(os.Environ(), "GH_HOST=github.com", "GH_PROMPT_DISABLED=1")
		cmd.Dir = root
		cmd.Stdin = bytes.NewReader(input)
		out := reviewOutput{cancel: cancel}
		cmd.Stdout = &out
		// Do not expose credential-bearing child stderr or request bodies.
		if err := ownedcmd.Run(ctx, cmd); err != nil {
			return nil, e.New(e.CodeGateUnmet, "maintenance %s operation failed", args[0])
		}
		return out.data, nil
	}
	return c
}

type reviewOutput struct {
	data   []byte
	cancel context.CancelFunc
}

func (b *reviewOutput) Write(p []byte) (int, error) {
	if len(p) > maxReviewBytes-len(b.data) {
		b.cancel()
		return 0, io.ErrShortBuffer
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
func (c reviewClient) api(ctx context.Context, endpoint string, payload, result any) error {
	args := []string{"gh", "api", endpoint}
	var input []byte
	if payload != nil {
		var err error
		input, err = json.Marshal(payload)
		if err != nil {
			return e.Wrap(e.CodeInternal, err, "encode review request")
		}
		args = append(args, "--method", "POST", "--input", "-")
	}
	raw, err := c.invoke(ctx, args, input)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, result); err != nil {
		return e.Wrap(e.CodeGateUnmet, err, "decode review response")
	}
	return nil
}
func (c reviewClient) repoAPI(ctx context.Context, endpoint string, payload, result any) error {
	return c.api(ctx, "repos/"+Repository+"/"+endpoint, payload, result)
}
func (c reviewClient) pages(ctx context.Context, endpoint string, result any) error {
	raw, err := c.invoke(ctx, []string{"gh", "api", "--paginate", "--slurp", "repos/" + Repository + "/" + endpoint}, nil)
	if err != nil {
		return err
	}
	// Keep every page, including an empty first page. A failed lookup never
	// becomes an empty result that would authorize creating a duplicate issue.
	var pages [][]json.RawMessage
	if err := json.Unmarshal(raw, &pages); err != nil {
		return e.Wrap(e.CodeGateUnmet, err, "decode paginated review response")
	}
	if pages == nil {
		return e.New(e.CodeGateUnmet, "missing review pages")
	}
	flat := []json.RawMessage{}
	for _, page := range pages {
		if page == nil {
			return e.New(e.CodeGateUnmet, "missing review page")
		}
		flat = append(flat, page...)
	}
	joined, err := json.Marshal(flat)
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "join review pages")
	}
	if err := json.Unmarshal(joined, result); err != nil {
		return e.Wrap(e.CodeGateUnmet, err, "decode review items")
	}
	return nil
}
