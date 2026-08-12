// Copyright (c) Red Hat, Inc.
// SPDX-License-Identifier: MPL-2.0

package common

import (
	"testing"

	v1 "kubevirt.io/api/core/v1"
)

func TestExtraMediaDevicesCDROMDefault(t *testing.T) {
	vols, disks := ExtraMediaDevices([]ExtraMediaAttachment{{DataVolume: "media-vol"}})
	if len(vols) != 1 || len(disks) != 1 {
		t.Fatalf("expected 1 volume + 1 disk, got %d/%d", len(vols), len(disks))
	}
	if disks[0].Name != "extramedia0" {
		t.Errorf("disk name = %q, want extramedia0", disks[0].Name)
	}
	if disks[0].DiskDevice.CDRom == nil {
		t.Fatalf("expected a read-only CD-ROM device by default")
	}
	if disks[0].DiskDevice.CDRom.Bus != v1.DiskBus("scsi") {
		t.Errorf("bus = %q, want scsi", disks[0].DiskDevice.CDRom.Bus)
	}
	if disks[0].BootOrder != nil {
		t.Errorf("extra media must not be a boot device")
	}
	if vols[0].Name != "extramedia0" || vols[0].DataVolume == nil || vols[0].DataVolume.Name != "media-vol" {
		t.Errorf("volume did not reference the DataVolume: %+v", vols[0])
	}
}

func TestExtraMediaDevicesDiskAndOverrides(t *testing.T) {
	_, disks := ExtraMediaDevices([]ExtraMediaAttachment{
		{DataVolume: "m", As: "disk", Name: "payload", Bus: "virtio"},
	})
	if disks[0].Name != "payload" {
		t.Errorf("disk name = %q, want payload", disks[0].Name)
	}
	if disks[0].DiskDevice.Disk == nil {
		t.Fatalf("expected a plain disk device when as=disk")
	}
	if disks[0].DiskDevice.Disk.Bus != v1.DiskBus("virtio") {
		t.Errorf("bus = %q, want virtio", disks[0].DiskDevice.Disk.Bus)
	}
}

func TestExtraMediaDeviceNameGeneration(t *testing.T) {
	if got := ExtraMediaDeviceName(ExtraMediaAttachment{}, 2); got != "extramedia2" {
		t.Errorf("generated name = %q, want extramedia2", got)
	}
	if got := ExtraMediaDeviceName(ExtraMediaAttachment{Name: "given"}, 0); got != "given" {
		t.Errorf("explicit name = %q, want given", got)
	}
}
