// Copyright (c) Red Hat, Inc.
// SPDX-License-Identifier: MPL-2.0

package iso

import (
	"context"
	"fmt"
	"time"

	"github.com/hashicorp/packer-plugin-sdk/multistep"
	"github.com/hashicorp/packer-plugin-sdk/packer"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"

	v1 "kubevirt.io/api/core/v1"
	"kubevirt.io/client-go/kubecli"
)

type StepStopVirtualMachine struct {
	Config Config
	Client kubecli.KubevirtClient
}

func (s *StepStopVirtualMachine) Run(ctx context.Context, state multistep.StateBag) multistep.StepAction {
	ui := state.Get("ui").(packer.Ui)
	name := s.Config.Name
	namespace := s.Config.Namespace

	ui.Sayf("Stopping the temporary VirtualMachine (%s/%s)...", namespace, name)

	vm, err := s.Client.VirtualMachine(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		state.Put("error", err)
		ui.Error(err.Error())
		return multistep.ActionHalt
	}
	vm.Spec.RunStrategy = ptr.To(v1.RunStrategyHalted)

	_, err = s.Client.VirtualMachine(vm.Namespace).Update(ctx, vm, metav1.UpdateOptions{})
	if err != nil {
		state.Put("error", err)
		ui.Error(err.Error())
		return multistep.ActionHalt
	}
	if err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 10*time.Minute, true, func(ctx context.Context) (bool, error) {
		_, err := s.Client.VirtualMachineInstance(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}); err != nil {
		err = fmt.Errorf("wait for VirtualMachineInstance %s/%s to stop: %w", namespace, name, err)
		state.Put("error", err)
		ui.Error(err.Error())
		return multistep.ActionHalt
	}
	state.Put(stateTemporaryVMDetached, true)
	return multistep.ActionContinue
}

func (s *StepStopVirtualMachine) Cleanup(state multistep.StateBag) {
	// Left blank intentionally
}
