// Copyright IBM Corp. 2013, 2026
// SPDX-License-Identifier: MPL-2.0

package iso

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/packer-plugin-kubevirt/builder/kubevirt/iso/staging"
	"github.com/hashicorp/packer-plugin-sdk/multistep"
	"github.com/hashicorp/packer-plugin-sdk/packer"
)

const (
	stateISOStagingResult = "iso_staging_result"
	stateISOVolumeName    = "iso_volume_name"
)

// StepStageISO resolves the configured installation-media source into a single
// CDI DataVolume and publishes its name for VM creation. Plugin-created media is
// removed during cleanup unless it is retained or still attached to the VM.
type StepStageISO struct {
	Config  Config
	Manager *staging.Manager
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
		Retain:            s.Config.IsoRetain,
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
	if result.Retain || s.Config.KeepVM {
		ui.Sayf("Retaining managed ISO DataVolume (%s/%s).", s.Config.Namespace, result.VolumeName)
		return
	}
	// Only delete the imported media on a clean build. A halted or cancelled
	// build keeps it so a re-run reuses the (often multi-GB) import instead of
	// downloading it again. This mirrors Packer's common output-dir step, which
	// preserves its output on cancel/halt.
	_, cancelled := state.GetOk(multistep.StateCancelled)
	_, halted := state.GetOk(multistep.StateHalted)
	if cancelled || halted {
		ui.Sayf(
			"Build did not succeed; retaining managed ISO DataVolume (%s/%s) for reuse. "+
				"Delete it with `packer build -force`, or `kubectl -n %s delete dv %s`, to force a re-import.",
			s.Config.Namespace, result.VolumeName, s.Config.Namespace, result.VolumeName)
		return
	}
	// Reverse-order cleanup runs after VM deletion. If the temporary VM was
	// created but never confirmed detached, keep the media so we never delete a
	// DataVolume a lingering VMI still references.
	if created, _ := state.Get(stateTemporaryVMCreated).(bool); created {
		if detached, _ := state.Get(stateTemporaryVMDetached).(bool); !detached {
			ui.Errorf(
				"Retaining managed ISO DataVolume %s/%s because the temporary VM did not fully detach",
				s.Config.Namespace, result.VolumeName)
			return
		}
	}

	ui.Sayf("Deleting managed ISO DataVolume (%s/%s)...", s.Config.Namespace, result.VolumeName)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := s.Manager.Cleanup(ctx, s.Config.Namespace, result); err != nil {
		ui.Errorf("Failed to clean managed ISO DataVolume: %v", err)
	}
}
