// Copyright (c) Red Hat, Inc.
// SPDX-License-Identifier: MPL-2.0

package common

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "kubevirt.io/api/core/v1"
	"kubevirt.io/client-go/kubecli"
)

// ExtraMediaAttachment is a builder-agnostic description of one additional,
// read-only medium to attach to the temporary build VM. Each builder converts
// its own HCL config block into this shape so the attach/preflight logic is
// shared.
type ExtraMediaAttachment struct {
	// DataVolume is the name of an existing CDI DataVolume to attach.
	DataVolume string
	// As is the device kind: "cdrom" (default, read-only) or "disk".
	As string
	// Name is the disk device name; a collision-free default is generated when empty.
	Name string
	// Bus is the device bus; defaults to "scsi".
	Bus string
}

// ExtraMediaDeviceName returns the device name for the i-th entry, generating a
// default when none is set.
func ExtraMediaDeviceName(m ExtraMediaAttachment, i int) string {
	if m.Name != "" {
		return m.Name
	}
	return fmt.Sprintf("extramedia%d", i)
}

// ExtraMediaDevices builds the read-only volumes and disks for the given media,
// to be appended to a temporary VM's disks/volumes. CD-ROM devices are
// read-only by default, the media is never a boot device (no BootOrder), and it
// is never part of the captured image.
func ExtraMediaDevices(items []ExtraMediaAttachment) ([]v1.Volume, []v1.Disk) {
	var volumes []v1.Volume
	var disks []v1.Disk
	for i, m := range items {
		name := ExtraMediaDeviceName(m, i)
		bus := m.Bus
		if bus == "" {
			bus = "scsi"
		}
		disk := v1.Disk{Name: name}
		if m.As == "disk" {
			disk.DiskDevice = v1.DiskDevice{Disk: &v1.DiskTarget{Bus: v1.DiskBus(bus)}}
		} else {
			// A CD-ROM is read-only by default, which is exactly what extra media wants.
			disk.DiskDevice = v1.DiskDevice{CDRom: &v1.CDRomTarget{Bus: v1.DiskBus(bus)}}
		}
		disks = append(disks, disk)
		volumes = append(volumes, v1.Volume{
			Name: name,
			VolumeSource: v1.VolumeSource{
				DataVolume: &v1.DataVolumeSource{Name: m.DataVolume},
			},
		})
	}
	return volumes, disks
}

// PreflightExtraMedia fails fast if any referenced extra-media DataVolume is
// absent from the namespace, so the build errors clearly instead of stalling on
// a missing volume at VM create. Content integrity of a staged DataVolume is
// the stager's responsibility (e.g. `harvester-image stage-iso`'s SHA-512 gate),
// not the plugin's; this only confirms the volume the pipeline promised exists.
func PreflightExtraMedia(ctx context.Context, client kubecli.KubevirtClient, namespace string, items []ExtraMediaAttachment) error {
	for _, m := range items {
		if _, err := client.CdiClient().CdiV1beta1().DataVolumes(namespace).Get(ctx, m.DataVolume, metav1.GetOptions{}); err != nil {
			return fmt.Errorf("resolve extra_media DataVolume %s/%s: %w", namespace, m.DataVolume, err)
		}
	}
	return nil
}
