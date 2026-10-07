// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

// Package client is the context-first embeddable SDK facade described by ADR-17.
//
// Use LoadDefinition, LoadEnvironment, PrepareLifecycleRun/ExecuteLifecycleRun or
// GenerateLifecycleRun, and VerifyRelease for the public lifecycle. ResolveBuild
// and Run support exact component/platform diagnostics. Domain packages own
// compilation, execution, publication, authentication and their authority checks;
// the facade exposes no separate qualified-evidence or publication workflow.
package client

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/NVIDIA/nke-recipes/pkg/nkd/catalog"
	e "github.com/NVIDIA/nke-recipes/pkg/nkd/errors"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/lifecycle"
	"github.com/NVIDIA/nke-recipes/pkg/nkd/pipeline"
)

// CatalogIO is the narrow context-aware read boundary used by catalog.LoadContext.
// It cannot replace catalog parsing or validation; it must honor cancellation and
// supplies only bytes and directory names. The facade defensively enforces catalog's
// file, aggregate, listing, and depth bounds on every returned value.
type CatalogIO interface {
	ReadFile(context.Context, string) ([]byte, error)
	ReadDir(context.Context, string) ([]string, error)
	Name() string
}

// AtomicOutput commits canonical document bytes under a caller-defined name.
// Implementations must make the commit atomic: on error or cancellation the previous
// destination remains unchanged; on nil the supplied bytes are durably committed.
// Concurrent commits to one name must use no-overwrite or compare-and-swap semantics,
// never last-writer-wins. The facade never reuses the supplied buffer.
type AtomicOutput interface {
	Commit(ctx context.Context, name string, canonicalBytes []byte) error
}

// Client is a small orchestration facade over the authoritative domain modules.
// Construct it with New and supply catalog data with WithData, WithProvider, or
// WithCatalogIO for producer resolution. Graph export and Release consumption are opt-in.
type Client struct {
	catalogIO CatalogIO
	runner    pipeline.Runner
	runtime   string

	output            AtomicOutput
	lifecycleConsumer *lifecycle.Consumer
	configErr         error
}

// Option configures a Client. Invalid injected dependencies are remembered and make
// every subsequent operation fail closed because New retains its v1 pointer return.
type Option func(*Client)

// WithReleaseConsumer configures the transport and verifier used by the
// simplified one-reference Release consumer for every later VerifyRelease call.
func WithReleaseConsumer(options lifecycle.ConsumerOptions) Option {
	return func(c *Client) {
		consumer, err := lifecycle.NewConsumer(options)
		if err != nil {
			c.setConfigError(fmt.Sprintf("release consumer: %v", err))
			return
		}
		c.lifecycleConsumer = consumer
	}
}

// WithData supplies a bounded on-disk catalog provider. It resolves
// every component beneath a held root without following symlinks and observes the
// operation context throughout reads and recursive listings.
func WithData(dir string) Option {
	return func(c *Client) {
		if dir == "" {
			c.setConfigError("catalog directory is empty")
			return
		}
		c.catalogIO = providerCatalogIO{provider: catalog.Dir(dir)}
	}
}

// WithProvider injects a catalog data provider. Authority-bearing SDK operations
// admit only providers that also implement catalog.ContextDataProvider.
func WithProvider(provider catalog.DataProvider) Option {
	return func(c *Client) {
		if provider == nil {
			c.setConfigError("catalog provider is nil")
			return
		}
		if _, ok := provider.(catalog.ContextDataProvider); !ok {
			c.setConfigError("catalog provider lacks bounded context-aware I/O; implement catalog.ContextDataProvider or use WithCatalogIO")
			return
		}
		c.catalogIO = providerCatalogIO{provider: provider}
	}
}

// WithCatalogIO injects context-aware catalog reads without replacing authoritative
// catalog parsing and validation.
func WithCatalogIO(catalogIO CatalogIO) Option {
	return func(c *Client) {
		if catalogIO == nil {
			c.setConfigError("catalog I/O is nil")
			return
		}
		c.catalogIO = catalogIO
	}
}

// WithRunner injects the authoritative execution engine adapter.
func WithRunner(runner pipeline.Runner) Option {
	return func(c *Client) {
		if runner == nil {
			c.setConfigError("pipeline runner is nil")
			return
		}
		c.runner = runner
	}
}

// WithRuntime sets the container runtime binary (docker | podman | …) supplied to runs
// that do not set one. It is runtime-neutral: the local runner only ever emits the common
// run/pull subcommands and flags, so podman is a near drop-in for docker.
func WithRuntime(binary string) Option {
	return func(c *Client) {
		if binary == "" {
			c.setConfigError("container runtime binary is empty")
			return
		}
		c.runtime = binary
	}
}

// WithAtomicOutput enables explicit canonical document publication.
func WithAtomicOutput(output AtomicOutput) Option {
	return func(c *Client) {
		if output == nil {
			c.setConfigError("atomic output is nil")
			return
		}
		c.output = output
	}
}

// New constructs a client with authoritative default modules and no implicit output
// destination.
func New(options ...Option) *Client {
	c := &Client{
		runner:  pipeline.Local{},
		runtime: "docker",
	}
	for _, option := range options {
		if option == nil {
			c.setConfigError("client option is nil")
			continue
		}
		option(c)
	}
	return c
}

// contextProvider adapts CatalogIO to catalog.Load's established DataProvider shape.
type contextProvider struct {
	ctx       context.Context
	catalogIO CatalogIO
}

func (provider contextProvider) ReadFile(name string) ([]byte, error) {
	return provider.ReadFileContext(provider.ctx, name)
}

func (provider contextProvider) ReadDir(name string) ([]string, error) {
	return provider.ReadDirContext(provider.ctx, name)
}

func (provider contextProvider) Name() string { return provider.catalogIO.Name() }

func (provider contextProvider) ReadFileContext(ctx context.Context, name string) ([]byte, error) {
	return provider.catalogIO.ReadFile(ctx, name)
}

func (provider contextProvider) ReadDirContext(ctx context.Context, name string) ([]string, error) {
	return provider.catalogIO.ReadDir(ctx, name)
}

// providerCatalogIO admits only the provider's deep context-aware seam. A legacy
// provider is not called and no unjoinable goroutine is used to make it appear
// cancelable.
type providerCatalogIO struct{ provider catalog.DataProvider }

func (provider providerCatalogIO) ReadFile(ctx context.Context, name string) ([]byte, error) {
	if err := contextError(ctx, "catalog read"); err != nil {
		return nil, err
	}
	contextProvider, ok := provider.provider.(catalog.ContextDataProvider)
	if !ok {
		return nil, e.New(e.CodeEngineCapability,
			"catalog provider %q has no bounded context-aware authority interface", provider.provider.Name())
	}
	contents, err := contextProvider.ReadFileContext(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := catalog.ValidateFilePayload(name, contents); err != nil {
		return nil, err
	}
	if err := contextError(ctx, "catalog read"); err != nil {
		return nil, err
	}
	return append([]byte(nil), contents...), nil
}

func (provider providerCatalogIO) ReadDir(ctx context.Context, name string) ([]string, error) {
	if err := contextError(ctx, "catalog directory read"); err != nil {
		return nil, err
	}
	contextProvider, ok := provider.provider.(catalog.ContextDataProvider)
	if !ok {
		return nil, e.New(e.CodeEngineCapability,
			"catalog provider %q has no bounded context-aware authority interface", provider.provider.Name())
	}
	names, err := contextProvider.ReadDirContext(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := catalog.ValidateDirectoryListing(name, names); err != nil {
		return nil, err
	}
	if err := contextError(ctx, "catalog directory read"); err != nil {
		return nil, err
	}
	result := append([]string(nil), names...)
	sort.Strings(result)
	return result, nil
}

func (provider providerCatalogIO) Name() string { return provider.provider.Name() }

func (c *Client) setConfigError(message string) {
	if c.configErr == nil {
		c.configErr = e.New(e.CodeUsage, "invalid client configuration: %s", message)
	}
}

func (c *Client) ready(ctx context.Context, operation string) error {
	if err := contextError(ctx, operation); err != nil {
		return err
	}
	if c == nil {
		return e.New(e.CodeUsage, "%s requires a client", operation)
	}
	if c.configErr != nil {
		return c.configErr
	}
	return nil
}

func (c *Client) commit(ctx context.Context, name string, canonicalBytes []byte) error {
	if name == "" {
		return nil
	}
	if err := c.requireAtomicOutput(name); err != nil {
		return err
	}
	if err := contextError(ctx, "output commit"); err != nil {
		return err
	}
	// AtomicOutput owns the commit point. A nil return means committed even if the
	// caller cancels immediately afterward; post-checking would report a false failure.
	return normalizeError(ctx, c.output.Commit(ctx, name, append([]byte(nil), canonicalBytes...)), "output commit")
}

func (c *Client) requireAtomicOutput(name string) error {
	if name != "" && c.output == nil {
		return e.New(e.CodeUsage, "output %q requested without an AtomicOutput", name)
	}
	return nil
}

func contextError(ctx context.Context, operation string) error {
	if ctx == nil {
		return e.New(e.CodeTimeout, "%s requires a non-nil context", operation)
	}
	if err := ctx.Err(); err != nil {
		return e.Wrap(e.CodeTimeout, err, "%s canceled", operation)
	}
	return nil
}

func normalizeError(ctx context.Context, err error, operation string) error {
	if err == nil {
		return nil
	}
	if ctx != nil && ctx.Err() != nil {
		return e.Wrap(e.CodeTimeout, ctx.Err(), "%s canceled", operation)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return e.Wrap(e.CodeTimeout, err, "%s canceled", operation)
	}
	return err
}
