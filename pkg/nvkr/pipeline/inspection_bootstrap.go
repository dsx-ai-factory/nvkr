// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package pipeline

import (
	"context"
	_ "embed"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dsx-ai-factory/nvkr/pkg/nvkr/canonical"
	e "github.com/dsx-ai-factory/nvkr/pkg/nvkr/errors"
)

//go:embed inspection_bootstrap.pl
var inspectionBootstrapProgram string

const inspectionBootstrapInterpreter = "/usr/bin/perl"

// InspectionBootstrapProgramDigest identifies the exact fixed launcher bytes.
// The Environment must pin these bytes alongside its in-image interpreter.
func InspectionBootstrapProgramDigest() string {
	return FileDigest([]byte(inspectionBootstrapProgram))
}

func inspectionBootstrapArgs(selection DiskInspectionSelection) []string {
	mode, digest, size := "probe", "-", "0"
	if !selection.bootstrapProbe {
		mode, digest, size = "execute", selection.streamBinding.Digest, strconv.FormatInt(selection.streamBinding.Size, 10)
	}
	return []string{"-f", "-e", inspectionBootstrapProgram, "--", mode, selection.Platform,
		selection.BootstrapInterpreterDigest, digest, size}
}

func validateInspectionBootstrap(selection DiskInspectionSelection) error {
	if selection.Tool == nil && selection.BootstrapInterpreterDigest == "" && selection.BootstrapProgramDigest == "" {
		return nil
	}
	if !canonical.IsDigest(selection.BootstrapInterpreterDigest) || selection.BootstrapProgramDigest != InspectionBootstrapProgramDigest() {
		return e.New(e.CodeUnpinnedInput, "produced inspection helper requires the exact interpreter and supported bootstrap program pins")
	}
	return nil
}

func verifyInspectionBootstrapContainer(selection DiskInspectionSelection, observed toolSmokeInspection) error {
	c, h := observed.Config, observed.HostConfig
	if c.User != "65534:65534" || c.Tty || !c.OpenStdin || c.Image != selection.Image ||
		!toolSmokeSameStrings(c.Env, []string{"PATH=/usr/bin:/bin", "HOME=/tmp", "LC_ALL=C", "TZ=UTC", "PERL5OPT=", "PERL5LIB=", "PERLLIB="}) ||
		h.PidMode != "" || h.UTSMode != "" || h.UsernsMode != "" || (h.IpcMode != "" && h.IpcMode != "private") ||
		(h.CgroupnsMode != "" && h.CgroupnsMode != "private") || len(c.Volumes) != 0 ||
		h.NetworkMode != "none" || !h.ReadonlyRootfs || h.Privileged || h.PublishAllPorts || h.Init == nil || !*h.Init ||
		len(h.GroupAdd)+len(h.VolumesFrom)+len(h.CapAdd)+len(h.Links)+len(h.ExtraHosts)+len(h.Dns)+len(h.DnsOptions)+len(h.DnsSearch)+len(h.DeviceCgroupRules)+len(h.Mounts)+len(h.Devices)+len(h.DeviceRequests)+len(h.PortBindings) != 0 ||
		!slices.Equal(h.CapDrop, []string{"ALL"}) || !slices.Equal(h.SecurityOpt, []string{"no-new-privileges"}) ||
		h.Memory != int64(selection.Resources.MemoryMiB<<20) || h.MemorySwap != h.Memory || h.CPUPeriod != 100000 || h.CPUQuota != int64(selection.Resources.CPUMillis*100) ||
		h.PidsLimit == nil || *h.PidsLimit != int64(diskInspectionPIDsLimit) || h.LogConfig.Type != "none" {
		return e.New(e.CodeEngineCapability, "inspection bootstrap differs from its confined resource profile")
	}
	if selection.scratchVolume == "" {
		if len(h.Binds) != 0 || len(h.Tmpfs) != 1 || h.Tmpfs["/tmp"] != "rw,nosuid,nodev,size="+strconv.FormatUint(selection.Resources.MemoryMiB, 10)+"m,mode=1777" {
			return e.New(e.CodeVerifyFailed, "inspection bootstrap changed its bounded scratch")
		}
	} else if !slices.Equal(h.Binds, []string{selection.scratchVolume + ":/tmp:nocopy"}) || len(h.Tmpfs) != 0 {
		return e.New(e.CodeVerifyFailed, "inspection bootstrap changed its private scratch volume")
	}
	if !slices.Equal(observed.Config.Entrypoint, []string{inspectionBootstrapInterpreter}) ||
		strings.Join(observed.Config.Cmd, "\x00") != selection.bootstrapCommand {
		return e.New(e.CodeVerifyFailed, "inspection container changed its measured bootstrap command")
	}
	for _, mount := range observed.Mounts {
		if mount.Destination == "/tmp" && ((mount.Type == "tmpfs" && selection.scratchVolume == "") ||
			(mount.Type == "volume" && selection.scratchVolume != "" && mount.Source != "")) {
			continue
		}
		return e.New(e.CodeVerifyFailed, "sealed inspection bootstrap admits no host mounts")
	}
	if len(observed.HostConfig.Binds) != 0 && selection.scratchVolume == "" {
		return e.New(e.CodeVerifyFailed, "sealed inspection bootstrap contains host bindings")
	}
	return nil
}

// admitInspectionBootstrap exercises the selected interpreter and kernel ABI in
// the immutable appliance before producer work. The same program seals, measures
// and executes an image-owned probe; no acquired helper or subject is needed.
func admitInspectionBootstrap(ctx context.Context, runtime packerContainerRuntime, selection DiskInspectionSelection, imageID string) (returnErr error) {
	if selection.BootstrapInterpreterDigest == "" {
		return nil
	}
	selection.Resources.MemoryMiB = 128
	selection.Resources.CPUMillis = min(selection.Resources.CPUMillis, 1000)
	selection.bootstrapProbe = true
	selection.bootstrapCommand = strings.Join(inspectionBootstrapArgs(selection), "\x00")
	attempt, err := packerAttemptID()
	if err != nil {
		return e.Wrap(e.CodeInternal, err, "allocating bootstrap probe")
	}
	uncertain := true
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		returnErr = errors.Join(returnErr, removeDiskInspectionContainer(cleanup, runtime, attempt, uncertain))
	}()
	var created, diagnostic, output diskInspectionBuffer
	created.limit, diagnostic.limit, output.limit = toolSmokeControlBytes, toolSmokeControlBytes, toolSmokeControlBytes
	args := diskInspectionCreateArgs(selection, selection.Image, "nvkr-bootstrap-"+attempt, attempt, nil)
	if err := runtime.Run(ctx, nil, &created, &diagnostic, diskInspectionCommandArgs(runtime.RuntimeName(), args...)...); err != nil {
		return runtimeCommandError(ctx, err, diskInspectionDiagnostic(diagnostic.Bytes()), e.CodeEngineCapability, "creating inspection bootstrap probe")
	}
	uncertain = false
	ids := strings.Fields(created.String())
	if diagnostic.Len() != 0 || len(ids) != 1 || !canonical.IsDigest("sha256:"+ids[0]) {
		return e.New(e.CodeEngineCapability, "bootstrap probe lacks a unique confined container")
	}
	if err := observeDiskInspectionImage(ctx, runtime, selection, ids[0], imageID); err != nil {
		return err
	}
	diagnostic.Reset()
	if err := runtime.Run(ctx, nil, &output, &diagnostic, diskInspectionCommandArgs(runtime.RuntimeName(), "start", "--attach", "--interactive", ids[0])...); err != nil {
		return runtimeCommandError(ctx, err, diskInspectionDiagnostic(diagnostic.Bytes()), e.CodeEngineCapability, "admitting sealed inspection execution")
	}
	if diagnostic.Len() != 0 || output.String() != "nvkr-sealed-exec-v1\n" {
		return e.New(e.CodeEngineCapability, "inspection bootstrap did not prove sealed descriptor execution")
	}
	return nil
}
