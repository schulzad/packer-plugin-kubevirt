// Copyright (c) Red Hat, Inc.
// SPDX-License-Identifier: MPL-2.0

package common

import (
	"context"
	"fmt"

	v1 "kubevirt.io/api/core/v1"
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
	// SHA512 optionally pins the media content digest (bare lowercase SHA-512
	// hex). When set and the DataVolume is stage-managed, it must equal the
	// harvester-image-tools/stage-content-sha512 marker or preflight fails closed.
	SHA512 string
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

// PreflightExtraMedia blocks until every referenced extra-media DataVolume is
// ready for the temporary build VM to attach, so the build errors clearly (or
// waits explicitly) instead of stalling on a missing volume — or silently
// attaching a blank one — at VM create. It fails fast when a referenced
// DataVolume is absent.
//
// For a stage-iso DataVolume, readiness gates on the stage-complete marker (never
// CDI phase, which reports Succeeded on a blank Block volume before its bytes are
// staged); any other DataVolume falls back to ordinary CDI-phase readiness. When
// an entry carries an expected SHA512, it is pinned against the stage marker.
// Assembling and staging the content remain the pipeline's job (e.g.
// `harvester-image stage-iso`'s SHA-512 gate); this only consumes what exists.
func PreflightExtraMedia(ctx context.Context, client MediaReadyClient, namespace string, items []ExtraMediaAttachment, progress func(format string, args ...any)) error {
	for _, m := range items {
		if err := WaitUntilMediaReady(ctx, client, namespace, m.DataVolume, MediaReadyOptions{
			ExpectedSHA512: m.SHA512,
			Progress:       progress,
		}); err != nil {
			return fmt.Errorf("extra_media: %w", err)
		}
	}
	return nil
}
