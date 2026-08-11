// Copyright (c) Red Hat, Inc.
// SPDX-License-Identifier: MPL-2.0

package image

import (
	"context"
	"fmt"
	"time"

	kubevirtcommon "github.com/hashicorp/packer-plugin-kubevirt/builder/kubevirt/common"
	"github.com/hashicorp/packer-plugin-sdk/multistep"
	"github.com/hashicorp/packer-plugin-sdk/packer"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	ptr "k8s.io/utils/ptr"

	v1 "kubevirt.io/api/core/v1"
	instancetypeapi "kubevirt.io/api/instancetype"
	"kubevirt.io/client-go/kubecli"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
)

const (
	stateTemporaryVMCreated  = "temporary_vm_created"
	stateTemporaryVMDetached = "temporary_vm_detached"
)

// StepCreateVM creates the temporary VM whose root disk is a full clone of the
// configured source DataSource, boots it directly (no ISO, no CD-ROM, no
// install), and waits for it to become Ready. The captured, provisioned disk is
// finalized by the reused StepCreateBootableVolume.
type StepCreateVM struct {
	Config Config
	Client kubecli.KubevirtClient
}

func (s *StepCreateVM) Run(ctx context.Context, state multistep.StateBag) multistep.StepAction {
	ui := state.Get("ui").(packer.Ui)
	name := s.Config.Name
	namespace := s.Config.Namespace

	// Fail fast with a clear message if the base DataSource is missing, rather
	// than letting the DataVolume clone stall.
	if _, err := s.Client.CdiClient().CdiV1beta1().DataSources(s.Config.SourceNamespace).Get(ctx, s.Config.SourceDataSource, metav1.GetOptions{}); err != nil {
		err = fmt.Errorf("resolve base DataSource %s/%s: %w", s.Config.SourceNamespace, s.Config.SourceDataSource, err)
		state.Put("error", err)
		ui.Error(err.Error())
		return multistep.ActionHalt
	}

	virtualMachine := imageVirtualMachine(s.Config, forwardPortsFor(s.Config))

	ui.Sayf("Creating a temporary VirtualMachine (%s/%s) from base DataSource %s/%s...",
		namespace, name, s.Config.SourceNamespace, s.Config.SourceDataSource)

	if _, err := s.Client.VirtualMachine(namespace).Create(ctx, virtualMachine, metav1.CreateOptions{}); err != nil {
		if _, getErr := s.Client.VirtualMachine(namespace).Get(ctx, name, metav1.GetOptions{}); getErr == nil || !apierrors.IsNotFound(getErr) {
			state.Put(stateTemporaryVMCreated, true)
		}
		state.Put("error", err)
		ui.Error(err.Error())
		return multistep.ActionHalt
	}
	state.Put(stateTemporaryVMCreated, true)

	if err := s.waitUntilReady(ctx); err != nil {
		state.Put("error", err)
		ui.Error(err.Error())
		return multistep.ActionHalt
	}
	return multistep.ActionContinue
}

func (s *StepCreateVM) Cleanup(state multistep.StateBag) {
	ui := state.Get("ui").(packer.Ui)
	name := s.Config.Name
	namespace := s.Config.Namespace

	if s.Config.KeepVM {
		ui.Sayf("Keeping VirtualMachine (%s/%s).", namespace, name)
		return
	}

	ui.Sayf("Deleting VirtualMachine (%s/%s)...", namespace, name)
	if err := s.Client.VirtualMachine(namespace).Delete(context.Background(), name, metav1.DeleteOptions{
		GracePeriodSeconds: ptr.To(int64(0)),
	}); err != nil && !apierrors.IsNotFound(err) {
		ui.Errorf("Failed to delete VirtualMachine %s/%s: %v", namespace, name, err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := wait.PollUntilContextTimeout(ctx, 2*time.Second, 5*time.Minute, true, func(ctx context.Context) (bool, error) {
		_, vmErr := s.Client.VirtualMachine(namespace).Get(ctx, name, metav1.GetOptions{})
		_, vmiErr := s.Client.VirtualMachineInstance(namespace).Get(ctx, name, metav1.GetOptions{})
		vmGone := apierrors.IsNotFound(vmErr)
		vmiGone := apierrors.IsNotFound(vmiErr)
		if vmErr != nil && !vmGone {
			return false, vmErr
		}
		if vmiErr != nil && !vmiGone {
			return false, vmiErr
		}
		return vmGone && vmiGone, nil
	}); err != nil {
		ui.Errorf("Timed out waiting for VirtualMachine %s/%s to detach: %v", namespace, name, err)
		return
	}
	state.Put(stateTemporaryVMDetached, true)
}

func (s *StepCreateVM) waitUntilReady(ctx context.Context) error {
	name := s.Config.Name
	namespace := s.Config.Namespace
	timeout := s.Config.BootTimeout
	if timeout <= 0 {
		timeout = time.Hour
	}
	err := wait.PollUntilContextTimeout(ctx, 5*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		vm, err := s.Client.VirtualMachine(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return vm.Status.Ready, nil
	})
	if err != nil {
		return fmt.Errorf("wait for VirtualMachine %s/%s to become Ready: %w", namespace, name, err)
	}
	return nil
}

func forwardPortsFor(c Config) []v1.Port {
	switch c.Communicator {
	case "winrm":
		port := c.WinRMRemotePort
		if port == 0 {
			port = 5985
		}
		return []v1.Port{{Name: "winrm", Port: int32(port), Protocol: "TCP"}}
	case "ssh":
		port := c.SSHRemotePort
		if port == 0 {
			port = 22
		}
		return []v1.Port{{Name: "ssh", Port: int32(port), Protocol: "TCP"}}
	default:
		return nil
	}
}

func imageVirtualMachine(c Config, forwardPorts []v1.Port) *v1.VirtualMachine {
	instanceTypeKind := c.InstanceTypeKind
	if instanceTypeKind == "" {
		instanceTypeKind = instancetypeapi.ClusterSingularResourceName
	}
	preferenceKind := c.PreferenceKind
	if preferenceKind == "" {
		preferenceKind = instancetypeapi.ClusterSingularPreferenceResourceName
	}

	diskBus := c.DiskInterface
	if diskBus == "" {
		diskBus = "virtio"
	}
	rootBootOrder := uint(1)

	vmNetworks := make([]v1.Network, len(c.Networks))
	vmInterfaces := make([]v1.Interface, len(c.Networks))
	for i, n := range c.Networks {
		vmNetworks[i], vmInterfaces[i] = convertToNetwork(n, forwardPorts)
	}

	sourceNamespace := c.SourceNamespace
	vm := &v1.VirtualMachine{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v1.GroupVersion.String(),
			Kind:       "VirtualMachine",
		},
		ObjectMeta: metav1.ObjectMeta{Name: c.Name},
		Spec: v1.VirtualMachineSpec{
			RunStrategy: ptr.To(kubevirtcommon.RunStrategyForShutdownCommand(c.ShutdownCommand)),
			DataVolumeTemplates: []v1.DataVolumeTemplateSpec{
				{
					ObjectMeta: metav1.ObjectMeta{Name: c.Name + "-rootdisk"},
					Spec: cdiv1.DataVolumeSpec{
						// Clone the base image into a new, full, independent root
						// disk. sourceRef to a DataSource is the golden-image path
						// produced by the kubevirt-iso builder.
						SourceRef: &cdiv1.DataVolumeSourceRef{
							Kind:      cdiv1.DataVolumeDataSource,
							Name:      c.SourceDataSource,
							Namespace: &sourceNamespace,
						},
						PVC: &corev1.PersistentVolumeClaimSpec{
							Resources: corev1.VolumeResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceStorage: resource.MustParse(c.DiskSize),
								},
							},
							AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
							VolumeMode:  ptr.To(corev1.PersistentVolumeBlock),
						},
					},
				},
			},
			Template: &v1.VirtualMachineInstanceTemplateSpec{
				Spec: v1.VirtualMachineInstanceSpec{
					Networks: vmNetworks,
					Domain: v1.DomainSpec{
						Devices: v1.Devices{
							Interfaces: vmInterfaces,
							Disks: []v1.Disk{
								{
									Name:      "rootdisk",
									BootOrder: &rootBootOrder,
									DiskDevice: v1.DiskDevice{
										Disk: &v1.DiskTarget{Bus: v1.DiskBus(diskBus)},
									},
								},
							},
						},
					},
					Volumes: []v1.Volume{
						{
							Name: "rootdisk",
							VolumeSource: v1.VolumeSource{
								DataVolume: &v1.DataVolumeSource{Name: c.Name + "-rootdisk"},
							},
						},
					},
				},
			},
		},
	}

	if c.InstanceType != "" {
		vm.Spec.Instancetype = &v1.InstancetypeMatcher{Kind: instanceTypeKind, Name: c.InstanceType}
	} else {
		if c.CPUSockets > 0 || c.CPUCores > 0 || c.CPUThreads > 0 {
			vm.Spec.Template.Spec.Domain.CPU = &v1.CPU{
				Sockets: c.CPUSockets,
				Cores:   c.CPUCores,
				Threads: c.CPUThreads,
			}
		}
		if c.Memory != "" {
			vm.Spec.Template.Spec.Domain.Memory = &v1.Memory{Guest: ptr.To(resource.MustParse(c.Memory))}
		}
	}

	if c.Preference != "" {
		vm.Spec.Preference = &v1.PreferenceMatcher{Kind: preferenceKind, Name: c.Preference}
	}

	return vm
}

func convertToNetwork(n Network, forwardPorts []v1.Port) (v1.Network, v1.Interface) {
	vmNetwork := v1.Network{Name: n.Name}
	vmInterface := v1.Interface{Name: n.Name}

	switch {
	case n.Pod != nil:
		vmNetwork.NetworkSource.Pod = &v1.PodNetwork{
			VMNetworkCIDR:     n.Pod.VMNetworkCIDR,
			VMIPv6NetworkCIDR: n.Pod.VMIPv6NetworkCIDR,
		}
		vmInterface.InterfaceBindingMethod.Masquerade = &v1.InterfaceMasquerade{}
		vmInterface.Ports = forwardPorts
	case n.Multus != nil:
		vmNetwork.NetworkSource.Multus = &v1.MultusNetwork{
			NetworkName: n.Multus.NetworkName,
			Default:     n.Multus.Default,
		}
		vmInterface.InterfaceBindingMethod.Bridge = &v1.InterfaceBridge{}
	}
	return vmNetwork, vmInterface
}
