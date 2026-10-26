// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package nvkedemo

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/internal/documentpath"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	nvkrerr "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/siteidentity"
	"github.com/spiffe/go-spiffe/v2/spiffeid"
	"sigs.k8s.io/yaml"
)

type nativeSiteIdentityFailure struct{ cause error }

func (e *nativeSiteIdentityFailure) Error() string {
	return "site host identity handoff failed; private evidence retained"
}
func (e *nativeSiteIdentityFailure) Unwrap() error { return e.cause }

type nativeStaticEntry struct {
	APIVersion string `json:"apiVersion"`
	Kind       string
	Metadata   struct {
		Name, UID string
		Labels    map[string]string
	}
	Spec   json.RawMessage
	Status struct {
		Rendered, Masked *bool
		Set              *bool
	}
}
type nativeStaticEntrySpec struct {
	ClassName, ParentID, SPIFFEID string
	Selectors                     []string
}
type nativeSpireID struct {
	TrustDomain string `json:"trust_domain"`
	Path        string `json:"path"`
}

func (id nativeSpireID) String() string { return "spiffe://" + id.TrustDomain + id.Path }

type nativeSpireAgent struct {
	ID              nativeSpireID `json:"id"`
	AttestationType string        `json:"attestation_type"`
	Banned          *bool         `json:"banned"`
	ExpiresAt       json.Number   `json:"x509svid_expires_at"`
	Serial          json.Number   `json:"x509svid_serial_number"`
}
type nativeSpireAgents struct {
	Agents        []nativeSpireAgent `json:"agents"`
	NextPageToken string             `json:"next_page_token"`
}
type nativeSpireRegistration struct {
	ID                string                         `json:"id"`
	SPIFFEID          nativeSpireID                  `json:"spiffe_id"`
	ParentID          nativeSpireID                  `json:"parent_id"`
	Selectors         []struct{ Type, Value string } `json:"selectors"`
	Admin, Downstream bool
}
type nativeSpireRegistrations struct {
	Entries       []nativeSpireRegistration `json:"entries"`
	NextPageToken string                    `json:"next_page_token"`
}
type nativeIdentityObservation struct {
	entries       []nativeStaticEntry
	allowlist     map[string]string
	agents        nativeSpireAgents
	registrations nativeSpireRegistrations
}

// Each input below is a bounded description returned by an admitted CLI. The
// complete operation owner, not this data projection, issues the final receipt.
func nativeIdentityJSON(raw []byte, out any) error {
	if len(raw) > 1<<20 {
		return errors.New("site identity observation exceeds its byte bound")
	}
	if err := canonical.ValidateJSONDocument(raw); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(out)
}
func nativeSourceStaticEntry(raw []byte, tpm string) (nativeStaticEntry, nativeStaticEntrySpec, error) {
	if len(raw) > 1<<20 {
		return nativeStaticEntry{}, nativeStaticEntrySpec{}, errors.New("source host policy exceeds its byte bound")
	}
	// Describe the source export locally. Server-side CRD validation remains at
	// the later exactly-once create, after the live identity context is checked.
	if _, err := documentpath.Decode(raw); err != nil {
		return nativeStaticEntry{}, nativeStaticEntrySpec{}, fmt.Errorf("decode source host policy document: %w", err)
	}
	raw, err := yaml.YAMLToJSONStrict(raw)
	if err != nil {
		return nativeStaticEntry{}, nativeStaticEntrySpec{}, fmt.Errorf("convert source host policy document: %w", err)
	}
	var object struct {
		nativeStaticEntry
		Items []nativeStaticEntry
	}
	if err := nativeIdentityJSON(raw, &object); err != nil {
		return nativeStaticEntry{}, nativeStaticEntrySpec{}, err
	}
	entry := object.nativeStaticEntry
	if object.Kind == "List" {
		if object.APIVersion != "v1" || len(object.Items) != 1 {
			return entry, nativeStaticEntrySpec{}, errors.New("source export requires exactly one site host policy")
		}
		entry = object.Items[0]
	}
	var spec nativeStaticEntrySpec
	if entry.APIVersion != "spire.spiffe.io/v1alpha1" || entry.Kind != "ClusterStaticEntry" || entry.Metadata.Name == "" {
		return entry, spec, errors.New("source export is not a named host ClusterStaticEntry")
	}
	if err := nativeIdentityJSON(entry.Spec, &spec); err != nil {
		return entry, spec, err
	}
	peer, err := spiffeid.FromString(spec.SPIFFEID)
	if err != nil || peer.String() != spec.SPIFFEID || !strings.HasPrefix(peer.Path(), "/host/") {
		return entry, spec, errors.New("source export lacks its exact host SPIFFE identity")
	}
	parent, err := spiffeid.FromString(spec.ParentID)
	decoded, decodeErr := hex.DecodeString(tpm)
	if err != nil || parent.String() != spec.ParentID || parent.TrustDomain() != peer.TrustDomain() || decodeErr != nil || len(decoded) != 32 || hex.EncodeToString(decoded) != tpm || parent.Path() != "/spire/agent/tpm/"+tpm {
		return entry, spec, errors.New("source policy does not bind the prepared TPM identity")
	}
	if spec.ClassName == "" || !slices.Equal(spec.Selectors, []string{"unix:uid:0"}) {
		return entry, spec, errors.New("source host policy requires its class and root workload selector")
	}
	managed := 0
	for _, key := range []string{"nvke.nvidia.com/managed-by", "nke.nvidia.com/managed-by"} {
		if entry.Metadata.Labels[key] == "machine-spire-entry-controller" {
			managed++
		}
	}
	if managed != 1 {
		return entry, spec, errors.New("source policy requires its unambiguous machine controller ownership")
	}
	return entry, spec, nil
}
func nativeIdentityBaseline(source nativeStaticEntry, spec nativeStaticEntrySpec, before nativeIdentityObservation) error {
	if before.agents.NextPageToken != "" {
		return errors.New("baseline TPM agent observation is incomplete")
	}
	for _, entry := range before.entries {
		if entry.Metadata.Name == source.Metadata.Name {
			return errors.New("source host policy already exists; refuse implicit recovery or replacement")
		}
	}
	for _, agent := range before.agents.Agents {
		if agent.ID.String() == spec.ParentID {
			return errors.New("source TPM agent already exists; refuse implicit recovery")
		}
	}
	return nil
}
func nativeIdentitySourcePolicy(source, actual nativeStaticEntry) error {
	var expectedSpec, actualSpec map[string]any
	if err := nativeIdentityJSON(source.Spec, &expectedSpec); err != nil {
		return err
	}
	if err := nativeIdentityJSON(actual.Spec, &actualSpec); err != nil {
		return err
	}
	if !reflect.DeepEqual(expectedSpec, actualSpec) {
		return errors.New("permanent policy differs from the exact source export")
	}
	for key, value := range source.Metadata.Labels {
		if actual.Metadata.Labels[key] != value {
			return errors.New("permanent policy lost a source ownership label")
		}
	}
	return nil
}

// The authenticated API's create response joins the exact imported source bytes
// to one object identity. Later observations may not adopt a replacement object.
func nativeIdentityCreatedEntry(raw []byte, source nativeStaticEntry, tpm string) (string, error) {
	created, _, err := nativeSourceStaticEntry(raw, tpm)
	if err != nil {
		return "", err
	}
	if created.Metadata.Name != source.Metadata.Name || created.Metadata.UID == "" {
		return "", errors.New("source import returned no exact created CSE identity")
	}
	if err := nativeIdentitySourcePolicy(source, created); err != nil {
		return "", err
	}
	return created.Metadata.UID, nil
}

func nativeIdentityPolicy(source nativeStaticEntry, spec nativeStaticEntrySpec, tpm, createdUID string, observed nativeIdentityObservation) (string, string, error) {
	var found *nativeStaticEntry
	for i := range observed.entries {
		entry := &observed.entries[i]
		if entry.Metadata.Name == source.Metadata.Name {
			if found != nil {
				return "", "", errors.New("ambiguous permanent host policy")
			}
			found = entry
		}
	}
	if found == nil || found.Metadata.UID == "" || found.Status.Rendered == nil || !*found.Status.Rendered || found.Status.Masked == nil || *found.Status.Masked {
		return "", "", errors.New("permanent host policy has not rendered unmasked")
	}
	if createdUID == "" || found.Metadata.UID != createdUID {
		return "", "", errors.New("permanent host policy differs from the created CSE identity")
	}
	if err := nativeIdentitySourcePolicy(source, *found); err != nil {
		return "", "", err
	}
	var allowlistValue string
	for key, value := range source.Metadata.Labels {
		if value == "machine-spire-entry-controller" && (key == "nvke.nvidia.com/managed-by" || key == "nke.nvidia.com/managed-by") {
			allowlistValue = key + "=" + value
		}
	}
	if allowlistValue == "" || observed.allowlist[tpm] != allowlistValue {
		return "", "", errors.New("owning controller has not derived the selected TPM allowlist")
	}
	if observed.registrations.NextPageToken != "" {
		return "", "", errors.New("permanent registration observation is incomplete")
	}
	var registrationID string
	for _, entry := range observed.registrations.Entries {
		if entry.SPIFFEID.String() != spec.SPIFFEID || entry.ParentID.String() != spec.ParentID {
			continue
		}
		selectors := make([]string, 0, len(entry.Selectors))
		for _, selector := range entry.Selectors {
			selectors = append(selectors, selector.Type+":"+selector.Value)
		}
		slices.Sort(selectors)
		wanted := slices.Clone(spec.Selectors)
		slices.Sort(wanted)
		if registrationID != "" || entry.ID == "" || entry.Admin || entry.Downstream || !slices.Equal(selectors, wanted) {
			return "", "", errors.New("permanent host registration is ambiguous or differs from source policy")
		}
		registrationID = entry.ID
	}
	if registrationID == "" {
		return "", "", errors.New("permanent SPIRE has no exact source host registration")
	}
	return found.Metadata.UID, registrationID, nil
}

// Opaque outside this package. Only completeNativeSiteIdentity can return this
// after authenticating the selected artifacts and observing all live boundaries.
type nativeSiteIdentityReceipt struct {
	sourceSHA, helperSHA, cseUID, registrationID, agentSerial, bootID string
	observedAt                                                        time.Time
}

func nativeIdentityFreshTPM(spec nativeStaticEntrySpec, before, after nativeIdentityObservation, result siteidentity.Result, dispatched, now time.Time, expectedBoot string) (string, error) {
	if before.agents.NextPageToken != "" || after.agents.NextPageToken != "" {
		return "", errors.New("TPM agent observation is incomplete")
	}
	for _, agent := range before.agents.Agents {
		if agent.ID.String() == spec.ParentID {
			return "", errors.New("TPM agent existed before this operation")
		}
	}
	var serial string
	for _, agent := range after.agents.Agents {
		if agent.ID.String() != spec.ParentID {
			continue
		}
		expiry, err := agent.ExpiresAt.Int64()
		number, ok := new(big.Int).SetString(string(agent.Serial), 10)
		if serial != "" || agent.AttestationType != "tpm" || agent.Banned == nil || *agent.Banned || err != nil || expiry <= now.Add(time.Minute).Unix() || !ok || number.Sign() <= 0 {
			return "", errors.New("permanent TPM agent is ambiguous or lacks a usable newly issued identity")
		}
		serial = string(agent.Serial)
	}
	if serial == "" {
		return "", errors.New("permanent server has not attested the selected TPM agent")
	}
	proof := result.After
	if result.SchemaVersion != siteidentity.SchemaVersion || result.Operation != "handoff" || result.Completion != siteidentity.JoinedSuccess || result.FailureCode != "" || (result.Reconfiguration != "applied" && result.Reconfiguration != "unchanged") || result.Status == nil || !result.Status.ConfigApplied || !result.Status.HasValidSVID || result.Status.SPIFFEID != spec.SPIFFEID || result.Status.ExpiresAt <= now.Add(time.Minute).Unix() || proof == nil || result.Before.PeerID != spec.SPIFFEID || result.Before.BootID != expectedBoot || !siteidentity.FreshPeerProof(result.Before, *proof, dispatched, now) {
		return "", errors.New("fresh authenticated same-boot permanent host identity is unverified")
	}
	return serial, nil
}

// The accepted remote RPC may ignore caller cancellation. Keep the actual local
// helper/connection alive under one bounded drain; a joined local exit alone is
// not a remote join. The real caller uses the existing Runner with held stdin.
func captureNativeIdentity(ctx context.Context, budget time.Duration, call func(context.Context) ([]byte, error)) (siteidentity.Result, error) {
	var result siteidentity.Result
	if err := requireContext(ctx); err != nil {
		return result, err
	}
	critical, stop := context.WithTimeout(context.WithoutCancel(ctx), budget)
	defer stop()
	raw, commandErr := call(critical)
	if errors.Is(commandErr, ErrUnjoined) {
		return result, commandErr
	}
	raw = bytes.TrimSuffix(raw, []byte("\n"))
	if len(raw) > 64<<10 || canonical.UnmarshalExact(raw, &result) != nil || result.SchemaVersion != siteidentity.SchemaVersion {
		return result, errors.Join(ErrUnjoined, commandErr, errors.New("identity helper returned no accepted completion classification"))
	}
	switch result.Completion {
	case siteidentity.Unknown:
		return result, errors.Join(ErrUnjoined, commandErr, siteidentity.ErrRemoteCompletionUnknown)
	case siteidentity.NotStarted, siteidentity.JoinedFailure:
		return result, errors.Join(commandErr, nvkrerr.New(nvkrerr.CodePreflightFailed, "site identity helper did not complete the full handoff"), ctx.Err())
	case siteidentity.JoinedSuccess:
		if commandErr != nil || result.FailureCode != "" {
			return result, errors.Join(commandErr, errors.New("identity helper completion contradicts its command result"), ctx.Err())
		}
	default:
		return result, errors.Join(ErrUnjoined, commandErr, fmt.Errorf("identity helper has unknown completion classification %q", result.Completion))
	}
	return result, ctx.Err()
}

// This is the original bootstrap endpoint contract, rendered once for guest
// preparation and retained verbatim by its owning native attempt. It is not
// reconstructed from an observed private hash at handoff time.
func nativeBootstrapSpireEnvironment(cfg NativeSiteStartup) []byte {
	return []byte(fmt.Sprintf("SPIRE_SERVER_ADDRESS=%s\nSPIRE_SERVER_PORT=30081\nSPIRE_TRUST_DOMAIN=%s.nke.local\n", cfg.Gateway, cfg.Name))
}

func (s *nativeSiteAttempt) captureNativeIdentityMutation(ctx context.Context, budget time.Duration, call func(context.Context) ([]byte, error)) (result siteidentity.Result, resultErr error) {
	dispatched, joined := false, false
	defer func() {
		if dispatched && !joined {
			if !errors.Is(resultErr, ErrUnjoined) {
				resultErr = errors.Join(resultErr, ErrUnjoined)
			}
			if s.unjoinedWork == nil {
				s.unjoinedWork = resultErr
			}
		}
	}()
	result, resultErr = captureNativeIdentity(ctx, budget, func(critical context.Context) ([]byte, error) { dispatched = true; return call(critical) })
	joined = !errors.Is(resultErr, ErrUnjoined) && result.SchemaVersion == siteidentity.SchemaVersion && (result.Completion == siteidentity.NotStarted || result.Completion == siteidentity.JoinedFailure || result.Completion == siteidentity.JoinedSuccess)
	return result, resultErr
}
