// Copyright (c) Red Hat, Inc.
// SPDX-License-Identifier: MPL-2.0

package iso

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/packer-plugin-sdk/multistep"
	"github.com/hashicorp/packer-plugin-sdk/packer"

	"kubevirt.io/client-go/kubecli"
)

// StepValidateBootableVolume is a preflight that runs before the (long) install:
// it fails fast if the golden bootable volume objects already exist, unless the
// build was invoked with `packer build -force`. This mirrors the local builders'
// output-directory check and avoids wasting a full install only to collide on
// the output objects at the very end.
type StepValidateBootableVolume struct {
	Config Config
	Client kubecli.KubevirtClient
}

func (s *StepValidateBootableVolume) Run(ctx context.Context, state multistep.StateBag) multistep.StepAction {
	ui := state.Get("ui").(packer.Ui)

	// No final image is produced in this mode, so there is nothing to collide.
	if s.Config.SkipCreateImage {
		return multistep.ActionContinue
	}

	name := s.Config.Name
	namespace := s.Config.Namespace

	existing, err := bootableVolumeArtifactsExist(ctx, s.Client, namespace, name)
	if err != nil {
		state.Put("error", err)
		ui.Error(err.Error())
		return multistep.ActionHalt
	}
	if len(existing) == 0 {
		return multistep.ActionContinue
	}

	if !s.Config.PackerForce {
		err := fmt.Errorf(
			"bootable volume %q already exists in namespace %q (%s); delete it or re-run with `packer build -force` to overwrite",
			name, namespace, strings.Join(existing, ", "))
		state.Put("error", err)
		ui.Error(err.Error())
		return multistep.ActionHalt
	}

	ui.Sayf("Force mode: existing bootable volume objects (%s) will be replaced.", strings.Join(existing, ", "))
	return multistep.ActionContinue
}

func (s *StepValidateBootableVolume) Cleanup(state multistep.StateBag) {
	// Left blank intentionally
}
