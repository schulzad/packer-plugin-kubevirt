// Copyright (c) Red Hat, Inc.
// SPDX-License-Identifier: MPL-2.0

package iso

import (
	"os"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ptr "k8s.io/utils/ptr"

	v1 "kubevirt.io/api/core/v1"
	instancetypeapi "kubevirt.io/api/instancetype"
	cdiv1 "kubevirt.io/containerized-data-importer-api/pkg/apis/core/v1beta1"
)

const immediateBindingAnnotation = "cdi.kubevirt.io/storage.bind.immediate.requested"

func configMap(name string, mediaFiles []string) (*corev1.ConfigMap, error) {
	data := make(map[string]string)

	for _, path := range mediaFiles {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}

		filename := filepath.Base(path)
		data[filename] = string(content)
	}

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Data: data,
	}, nil
}

func virtualMachine(
	name,
	isoVolumeName,
	diskSize,
	instanceType,
	preferenceName,
	instanceTypeKind,
	preferenceKind,
	osType,
	diskBus,
	diskInterface string,
	cpuSockets,
	cpuCores,
	cpuThreads uint32,
	memory string,
	networks []Network,
	forwardPorts []v1.Port) *v1.VirtualMachine {
	var disks []v1.Disk
	var volumes []v1.Volume

	vmNetworks := make([]v1.Network, len(networks))
	vmInterfaces := make([]v1.Interface, len(networks))

	if instanceTypeKind == "" {
		instanceTypeKind = instancetypeapi.ClusterSingularResourceName
	}

	if preferenceKind == "" {
		preferenceKind = instancetypeapi.ClusterSingularPreferenceResourceName
	}

	if osType == "linux" {
		disks = getLinuxVirtualMachineDisks(diskBus, diskInterface)
		volumes = getLinuxVirtualMachineVolumes(name, isoVolumeName)
	}

	if osType == "windows" {
		disks = getWindowsVirtualMachineDisks(diskInterface)
		volumes = getWindowsVirtualMachineVolumes(name, isoVolumeName)
	}

	for i, n := range networks {
		vmNetworks[i], vmInterfaces[i] = convertToNetwork(n, forwardPorts)
	}

	vm := &v1.VirtualMachine{
		TypeMeta: metav1.TypeMeta{
			APIVersion: v1.GroupVersion.String(),
			Kind:       "VirtualMachine",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Spec: v1.VirtualMachineSpec{
			RunStrategy: ptr.To(v1.RunStrategyAlways),
			DataVolumeTemplates: []v1.DataVolumeTemplateSpec{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: name + "-rootdisk",
					},
					Spec: cdiv1.DataVolumeSpec{
						PVC: &corev1.PersistentVolumeClaimSpec{
							Resources: corev1.VolumeResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceName(corev1.ResourceStorage): resource.MustParse(diskSize),
								},
							},
							AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
							// Request a Block volume so the VM disk gets the full disk_size.
							// Without this, k8s defaults the PVC to Filesystem, which wraps
							// the disk in an ext4 image; CDI's filesystem overhead then
							// shrinks the usable virtual size well below disk_size (e.g. an
							// 80Gi request yields a ~74Gi disk).
							VolumeMode: ptr.To(corev1.PersistentVolumeBlock),
						},
						Source: &cdiv1.DataVolumeSource{
							Blank: &cdiv1.DataVolumeBlankImage{},
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
							Disks:      disks,
						},
					},
					Volumes: volumes,
				},
			},
		},
	}

	// Sizing is expressed either via an instancetype matcher OR via explicit
	// CPU/memory on the domain. KubeVirt forbids setting both at once.
	if instanceType != "" {
		vm.Spec.Instancetype = &v1.InstancetypeMatcher{
			Kind: instanceTypeKind,
			Name: instanceType,
		}
	} else {
		if cpuSockets > 0 || cpuCores > 0 || cpuThreads > 0 {
			vm.Spec.Template.Spec.Domain.CPU = &v1.CPU{
				Sockets: cpuSockets,
				Cores:   cpuCores,
				Threads: cpuThreads,
			}
		}
		if memory != "" {
			vm.Spec.Template.Spec.Domain.Memory = &v1.Memory{
				Guest: ptr.To(resource.MustParse(memory)),
			}
		}
	}

	// The preference is independent of sizing (it drives firmware, bus, and
	// device defaults such as Win11's UEFI + secure boot + TPM), so keep it
	// whenever one is provided.
	if preferenceName != "" {
		vm.Spec.Preference = &v1.PreferenceMatcher{
			Kind: preferenceKind,
			Name: preferenceName,
		}
	}

	return vm
}

func cloneVolume(name, namespace, diskSize string) *cdiv1.DataVolume {
	return &cdiv1.DataVolume{
		TypeMeta: metav1.TypeMeta{
			APIVersion: cdiv1.CDIGroupVersionKind.GroupVersion().String(),
			Kind:       "DataVolume",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Annotations: map[string]string{
				immediateBindingAnnotation: "true",
			},
		},
		Spec: cdiv1.DataVolumeSpec{
			Source: &cdiv1.DataVolumeSource{
				PVC: &cdiv1.DataVolumeSourcePVC{
					Name:      name + "-rootdisk",
					Namespace: namespace,
				},
			},
			PVC: &corev1.PersistentVolumeClaimSpec{
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceName(corev1.ResourceStorage): resource.MustParse(diskSize),
					},
				},
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				// Match the temporary VM's rootdisk: Block mode preserves the full
				// disk_size on the cloned golden volume (Filesystem mode would shrink it).
				VolumeMode: ptr.To(corev1.PersistentVolumeBlock),
			},
		},
	}
}

func sourceVolume(name, namespace, instanceType, preferenceName string) *cdiv1.DataSource {
	// Only advertise default-instancetype/default-preference labels that are
	// actually set. When the VM is sized via explicit cpu/memory there is no
	// instancetype to record.
	labels := map[string]string{}
	if instanceType != "" {
		labels["instancetype.kubevirt.io/default-instancetype"] = instanceType
	}
	if preferenceName != "" {
		labels["instancetype.kubevirt.io/default-preference"] = preferenceName
	}

	return &cdiv1.DataSource{
		TypeMeta: metav1.TypeMeta{
			APIVersion: cdiv1.CDIGroupVersionKind.GroupVersion().String(),
			Kind:       "DataSource",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
		Spec: cdiv1.DataSourceSpec{
			Source: cdiv1.DataSourceSource{
				PVC: &cdiv1.DataVolumeSourcePVC{
					Name:      name,
					Namespace: namespace,
				},
			},
		},
	}
}

func getLinuxVirtualMachineDisks(diskBus, diskInterface string) []v1.Disk {
	rootdisk := uint(1)
	cdrom := uint(2)
	oemdrv := uint(3)

	return []v1.Disk{
		{
			Name: "cdrom",
			DiskDevice: v1.DiskDevice{
				CDRom: &v1.CDRomTarget{
					Bus:  v1.DiskBus(diskBus),
					Tray: "closed",
				},
			},
			BootOrder: &cdrom,
		},
		{
			Name: "oemdrv",
			DiskDevice: v1.DiskDevice{
				CDRom: &v1.CDRomTarget{
					Bus:  v1.DiskBus(diskBus),
					Tray: "closed",
				},
			},
			BootOrder: &oemdrv,
		},
		{
			Name: "rootdisk",
			DiskDevice: v1.DiskDevice{
				Disk: &v1.DiskTarget{
					Bus: v1.DiskBus(diskInterface),
				},
			},
			BootOrder: &rootdisk,
		},
	}
}

func getLinuxVirtualMachineVolumes(name, isoVolumeName string) []v1.Volume {
	return []v1.Volume{
		{
			Name: "cdrom",
			VolumeSource: v1.VolumeSource{
				DataVolume: &v1.DataVolumeSource{
					Name: isoVolumeName,
				},
			},
		},
		{
			Name: "rootdisk",
			VolumeSource: v1.VolumeSource{
				DataVolume: &v1.DataVolumeSource{
					Name: name + "-rootdisk",
				},
			},
		},
		{
			Name: "oemdrv",
			VolumeSource: v1.VolumeSource{
				ConfigMap: &v1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: name,
					},
					VolumeLabel: "OEMDRV",
				},
			},
		},
	}
}

func getWindowsVirtualMachineDisks(diskInterface string) []v1.Disk {
	rootdisk := uint(1)
	cdrom := uint(2)

	return []v1.Disk{
		{
			Name: "cdrom",
			DiskDevice: v1.DiskDevice{
				CDRom: &v1.CDRomTarget{
					Bus: "sata",
				},
			},
			BootOrder: &cdrom,
		},
		{
			Name: "rootdisk",
			DiskDevice: v1.DiskDevice{
				Disk: &v1.DiskTarget{
					Bus: v1.DiskBus(diskInterface),
				},
			},
			BootOrder: &rootdisk,
		},
		{
			Name: "virtiocontainerdisk",
			DiskDevice: v1.DiskDevice{
				CDRom: &v1.CDRomTarget{
					Bus: "sata",
				},
			},
		},
		{
			Name: "sysprep",
			DiskDevice: v1.DiskDevice{
				CDRom: &v1.CDRomTarget{
					Bus: "sata",
				},
			},
		},
	}
}

func getWindowsVirtualMachineVolumes(name, isoVolumeName string) []v1.Volume {
	return []v1.Volume{
		{
			Name: "cdrom",
			VolumeSource: v1.VolumeSource{
				DataVolume: &v1.DataVolumeSource{
					Name: isoVolumeName,
				},
			},
		},
		{
			Name: "rootdisk",
			VolumeSource: v1.VolumeSource{
				DataVolume: &v1.DataVolumeSource{
					Name: name + "-rootdisk",
				},
			},
		},
		{
			Name: "sysprep",
			VolumeSource: v1.VolumeSource{
				Sysprep: &v1.SysprepSource{
					ConfigMap: &corev1.LocalObjectReference{
						Name: name,
					},
				},
			},
		},
		{
			Name: "virtiocontainerdisk",
			VolumeSource: v1.VolumeSource{
				ContainerDisk: &v1.ContainerDiskSource{
					Image: "quay.io/kubevirt/virtio-container-disk:v1.5.2",
				},
			},
		},
	}
}

func convertToNetwork(n Network, forwardPorts []v1.Port) (v1.Network, v1.Interface) {
	vmNetwork := v1.Network{Name: n.Name}
	vmInterface := v1.Interface{Name: n.Name}

	switch {
	case n.Pod != nil:
		// Pod network, and masquerade interface.
		vmNetwork.NetworkSource.Pod = &v1.PodNetwork{
			VMNetworkCIDR:     n.Pod.VMNetworkCIDR,
			VMIPv6NetworkCIDR: n.Pod.VMIPv6NetworkCIDR,
		}
		vmInterface.InterfaceBindingMethod.Masquerade = &v1.InterfaceMasquerade{}
		// KubeVirt masquerade only DNATs inbound ports that are explicitly
		// declared on the interface. Without this, the communicator port
		// (e.g. WinRM 5985 / SSH 22) never reaches the guest and the plugin's
		// port-forward times out. Bridge/Multus expose all ports directly, so
		// this is only required for the masquerade (pod) interface.
		vmInterface.Ports = forwardPorts
	case n.Multus != nil:
		// Multus network, and bridge interface.
		vmNetwork.NetworkSource.Multus = &v1.MultusNetwork{
			NetworkName: n.Multus.NetworkName,
			Default:     n.Multus.Default,
		}
		vmInterface.InterfaceBindingMethod.Bridge = &v1.InterfaceBridge{}
	}
	return vmNetwork, vmInterface
}
