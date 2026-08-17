// Copyright (c) Red Hat, Inc.
// SPDX-License-Identifier: MPL-2.0

package iso

import (
	"context"
	"fmt"
	"time"

	kubevirtcommon "github.com/hashicorp/packer-plugin-kubevirt/builder/kubevirt/common"
	"github.com/hashicorp/packer-plugin-sdk/multistep"
	"github.com/hashicorp/packer-plugin-sdk/packer"

	"kubevirt.io/client-go/kubecli"
)

type StepWaitForInstallation struct {
	Config Config
	Client kubecli.KubevirtClient
}

func (s *StepWaitForInstallation) Run(ctx context.Context, state multistep.StateBag) multistep.StepAction {
	ui := state.Get("ui").(packer.Ui)
	name := s.Config.Name
	namespace := s.Config.Namespace
	installationWaitTimeout := s.Config.InstallationWaitTimeout

	// wait_for_shutdown turns installation_wait_timeout from an unconditional
	// sleep into a bound on how long to wait for the guest to power ITSELF off,
	// so the image is captured the moment the install completes instead of after
	// a blind guess. The VM runs RunStrategy=RerunOnFailure, so a clean
	// self-power-off stays stopped for capture.
	if s.Config.WaitForShutdown {
		timeout := installationWaitTimeout
		if timeout <= 0 {
			timeout = time.Hour
		}
		ui.Sayf("Waiting up to %s for the guest to power itself off (install completion)...", timeout)
		if err := kubevirtcommon.WaitForGuestPowerOff(ctx, s.Client, namespace, name, timeout, 0, ui.Sayf); err != nil {
			err = fmt.Errorf("wait for guest to power off after install: %w (ensure the install ends in a power-off, not a reboot)", err)
			state.Put("error", err)
			ui.Error(err.Error())
			return multistep.ActionHalt
		}
		ui.Say("Guest powered off; capturing the installed disk.")
		return multistep.ActionContinue
	}

	if int64(installationWaitTimeout) > 0 {
		ui.Sayf("Waiting %s to complete ISO installation...", installationWaitTimeout.String())

		select {
		case <-time.After(installationWaitTimeout):
			break
		case <-ctx.Done():
			return multistep.ActionHalt
		}
	}
	return multistep.ActionContinue
}

func (s *StepWaitForInstallation) Cleanup(multistep.StateBag) {
	// Left blank intentionally
}
