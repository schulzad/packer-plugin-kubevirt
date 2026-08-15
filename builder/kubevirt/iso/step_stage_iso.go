// Copyright IBM Corp. 2013, 2026
// SPDX-License-Identifier: MPL-2.0

package iso

import (
	"context"
	"fmt"

	kubevirtcommon "github.com/hashicorp/packer-plugin-kubevirt/builder/kubevirt/common"
	"github.com/hashicorp/packer-plugin-kubevirt/builder/kubevirt/iso/staging"
	"github.com/hashicorp/packer-plugin-sdk/multistep"
	"github.com/hashicorp/packer-plugin-sdk/packer"

	"kubevirt.io/client-go/kubecli"
)

const (
	stateISOStagingResult = "iso_staging_result"
	stateISOVolumeName    = "iso_volume_name"
)

// StepStageISO resolves the configured installation-media source into a single
// CDI DataVolume and publishes its name for VM creation. Plugin-created media is
// kept after the build so later runs reuse the import; it is never deleted
// during cleanup. Externally managed DataVolumes are always preserved.
type StepStageISO struct {
	Config  Config
	Manager *staging.Manager
	Client  kubecli.KubevirtClient
}

func (s *StepStageISO) Run(ctx context.Context, state multistep.StateBag) multistep.StepAction {
	ui := state.Get("ui").(packer.Ui)
	if s.Manager == nil {
		err := fmt.Errorf("ISO staging manager is not configured")
		state.Put("error", err)
		ui.Error(err.Error())
		return multistep.ActionHalt
	}

	result, err := s.Manager.Stage(ctx, staging.Options{
		Namespace:         s.Config.Namespace,
		ExistingVolume:    s.Config.IsoVolumeName,
		HTTPURL:           s.Config.IsoURL,
		VolumeName:        s.Config.IsoStagingName,
		StorageSize:       s.Config.IsoStorageSize,
		StorageClass:      s.Config.IsoStorageClass,
		Checksum:          s.Config.IsoChecksum,
		HTTPSecretRef:     s.Config.IsoHTTPSecretRef,
		HTTPCertConfigMap: s.Config.IsoHTTPCertConfigMap,
		Timeout:           s.Config.IsoStagingTimeout,
		Force:             s.Config.PackerForce,
		Progress:          ui.Sayf,
	})
	state.Put(stateISOStagingResult, result)
	if err != nil {
		state.Put("error", err)
		ui.Error(err.Error())
		return multistep.ActionHalt
	}

	// An externally owned DataVolume may have been produced by `harvester-image
	// stage-iso`, which raw-populates a blank Block volume out of band: it reads
	// CDI phase Succeeded once bound, before its bytes are staged. Gate on the
	// stage-complete marker (falling back to CDI phase for a non-stage volume)
	// so the VM never boots a blank or half-staged CD-ROM. Managed HTTP imports
	// are already fully waited on by the staging manager.
	if result.Kind == staging.SourceExisting && s.Client != nil {
		if err := kubevirtcommon.WaitUntilMediaReady(ctx, s.Client, s.Config.Namespace, result.VolumeName, kubevirtcommon.MediaReadyOptions{
			ExpectedSHA512: s.Config.IsoDigest,
			Timeout:        s.Config.IsoStagingTimeout,
			Progress:       ui.Sayf,
		}); err != nil {
			state.Put("error", err)
			ui.Error(err.Error())
			return multistep.ActionHalt
		}
	}

	state.Put(stateISOVolumeName, result.VolumeName)
	ui.Sayf("Using ISO DataVolume (%s/%s).", s.Config.Namespace, result.VolumeName)
	return multistep.ActionContinue
}

func (s *StepStageISO) Cleanup(state multistep.StateBag) {
	if s.Manager == nil {
		return
	}
	rawResult, ok := state.GetOk(stateISOStagingResult)
	if !ok {
		return
	}
	result, ok := rawResult.(staging.Result)
	if !ok || result.VolumeName == "" {
		return
	}

	ui := state.Get("ui").(packer.Ui)
	if !result.Owned {
		if result.Kind == staging.SourceExisting {
			ui.Sayf("Preserving externally managed ISO DataVolume (%s/%s).", s.Config.Namespace, result.VolumeName)
		}
		return
	}
	// Managed installation media is always kept after the build, on every path,
	// so a later run reuses the (often multi-GB) import instead of downloading it
	// again. A stale volume is replaced on the next run with `packer build
	// -force`, or removed manually with kubectl.
	ui.Sayf(
		"Retaining managed ISO DataVolume (%s/%s) for reuse. "+
			"Re-import with `packer build -force`, or delete it with `kubectl -n %s delete dv %s`.",
		s.Config.Namespace, result.VolumeName, s.Config.Namespace, result.VolumeName)
}
