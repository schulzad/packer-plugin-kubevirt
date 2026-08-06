// Copyright (c) Red Hat, Inc.
// SPDX-License-Identifier: MPL-2.0

package iso

import (
	"context"
	"time"

	"github.com/hashicorp/packer-plugin-sdk/multistep"
	"github.com/hashicorp/packer-plugin-sdk/packer"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	ptr "k8s.io/utils/ptr"

	v1 "kubevirt.io/api/core/v1"
	"kubevirt.io/client-go/kubecli"
)

type StepCreateVirtualMachine struct {
	Config Config
	Client kubecli.KubevirtClient
}

func (s *StepCreateVirtualMachine) Run(ctx context.Context, state multistep.StateBag) multistep.StepAction {
	ui := state.Get("ui").(packer.Ui)
	name := s.Config.Name
	namespace := s.Config.Namespace
	isoVolumeName := s.Config.IsoVolumeName
	diskSize := s.Config.DiskSize
	instanceTypeName := s.Config.InstanceType
	instanceTypeKind := s.Config.InstanceTypeKind
	preferenceName := s.Config.Preference
	preferenceKind := s.Config.PreferenceKind
	osType := s.Config.OperatingSystemType
	diskBus := s.Config.DiskBus
	cpuSockets := s.Config.CPUSockets
	cpuCores := s.Config.CPUCores
	cpuThreads := s.Config.CPUThreads
	memory := s.Config.Memory
	networks := s.Config.Networks

	if osType == "" || (osType != "linux" && osType != "windows") {
		ui.Errorf("OS type of '%s' is not supported, set 'linux' or 'windows'.", osType)
		return multistep.ActionHalt
	}

	// KubeVirt masquerade forwards only explicitly declared ports to the guest,
	// so expose the active communicator's remote port. Without this the plugin's
	// port-forward can never reach WinRM/SSH inside the VM on a pod network.
	var forwardPorts []v1.Port
	switch s.Config.Communicator {
	case "winrm":
		port := s.Config.WinRMRemotePort
		if port == 0 {
			port = 5985
		}
		forwardPorts = []v1.Port{{Name: "winrm", Port: int32(port), Protocol: "TCP"}}
	case "ssh":
		port := s.Config.SSHRemotePort
		if port == 0 {
			port = 22
		}
		forwardPorts = []v1.Port{{Name: "ssh", Port: int32(port), Protocol: "TCP"}}
	}

	virtualMachine := virtualMachine(
		name,
		isoVolumeName,
		diskSize,
		instanceTypeName,
		preferenceName,
		instanceTypeKind,
		preferenceKind,
		osType,
		diskBus,
		cpuSockets,
		cpuCores,
		cpuThreads,
		memory,
		networks,
		forwardPorts)

	ui.Sayf("Creating a new temporary VirtualMachine (%s/%s)...", namespace, name)

	_, err := s.Client.VirtualMachine(namespace).Create(ctx, virtualMachine, metav1.CreateOptions{})
	if err != nil {
		ui.Error(err.Error())
		return multistep.ActionHalt
	}

	if err := s.waitUntilVirtualMachineReady(ctx); err != nil {
		return multistep.ActionHalt
	}
	return multistep.ActionContinue
}

func (s *StepCreateVirtualMachine) Cleanup(state multistep.StateBag) {
	ui := state.Get("ui").(packer.Ui)
	name := s.Config.Name
	namespace := s.Config.Namespace
	keepVM := s.Config.KeepVM

	if keepVM {
		ui.Sayf("Keeping VirtualMachine (%s/%s).", namespace, name)
		return
	}

	ui.Sayf("Deleting VirtualMachine (%s/%s)...", namespace, name)

	_ = s.Client.VirtualMachine(namespace).Delete(context.Background(), name, metav1.DeleteOptions{
		GracePeriodSeconds: ptr.To(int64(0)),
	})
}

func (s *StepCreateVirtualMachine) waitUntilVirtualMachineReady(ctx context.Context) error {
	name := s.Config.Name
	namespace := s.Config.Namespace
	pollInterval := 5 * time.Second
	pollTimeout := 3600 * time.Second
	poller := func(ctx context.Context) (bool, error) {
		vm, err := s.Client.VirtualMachine(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		if vm.Status.Ready {
			return true, nil
		}
		return false, nil
	}

	return wait.PollUntilContextTimeout(ctx, pollInterval, pollTimeout, true, poller)
}
