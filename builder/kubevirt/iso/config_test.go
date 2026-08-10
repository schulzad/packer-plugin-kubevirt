// Copyright IBM Corp. 2013, 2026
// SPDX-License-Identifier: MPL-2.0

package iso_test

import (
	"time"

	"github.com/hashicorp/packer-plugin-kubevirt/builder/kubevirt/iso"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Config ISO sources", func() {
	baseRaw := func() map[string]any {
		return map[string]any{
			"kube_config":               "/tmp/kubeconfig",
			"name":                      "rocky-image",
			"namespace":                 "images",
			"disk_size":                 "20Gi",
			"memory":                    "4Gi",
			"preference":                "rocky",
			"os_type":                   "linux",
			"installation_wait_timeout": "30m",
		}
	}

	It("accepts the backwards-compatible existing DataVolume source", func() {
		raw := baseRaw()
		raw["iso_volume_name"] = "rocky-install-iso"
		var config iso.Config

		_, err := config.Prepare(raw)

		Expect(err).NotTo(HaveOccurred())
		Expect(config.IsoVolumeName).To(Equal("rocky-install-iso"))
		Expect(config.IsoStagingName).To(BeEmpty())
	})

	It("defaults managed HTTP staging options", func() {
		raw := baseRaw()
		raw["iso_url"] = "https://mirror.example.test/rocky.iso"
		raw["iso_storage_size"] = "12Gi"
		var config iso.Config

		warnings, err := config.Prepare(raw)

		Expect(err).NotTo(HaveOccurred())
		Expect(config.IsoStagingName).To(Equal("rocky-image-iso"))
		Expect(config.IsoStagingTimeout).To(Equal(time.Hour))
		Expect(warnings).To(ContainElement(ContainSubstring("without iso_checksum")))
	})

	It("requires storage capacity for HTTP imports", func() {
		raw := baseRaw()
		raw["iso_url"] = "https://mirror.example.test/rocky.iso"
		var config iso.Config

		_, err := config.Prepare(raw)

		Expect(err).To(MatchError(ContainSubstring("iso_storage_size")))
	})

	It("rejects ambiguous ISO sources", func() {
		raw := baseRaw()
		raw["iso_volume_name"] = "existing"
		raw["iso_url"] = "https://mirror.example.test/rocky.iso"
		raw["iso_storage_size"] = "12Gi"
		var config iso.Config

		_, err := config.Prepare(raw)

		Expect(err).To(MatchError(ContainSubstring("exactly one")))
	})

	It("rejects malformed checksums", func() {
		raw := baseRaw()
		raw["iso_url"] = "https://mirror.example.test/rocky.iso"
		raw["iso_storage_size"] = "12Gi"
		raw["iso_checksum"] = "sha256:not-hex"
		var config iso.Config

		_, err := config.Prepare(raw)

		Expect(err).To(MatchError(ContainSubstring("iso_checksum")))
	})

	It("rejects staging names that collide with build disks", func() {
		raw := baseRaw()
		raw["iso_url"] = "https://mirror.example.test/rocky.iso"
		raw["iso_storage_size"] = "12Gi"
		raw["iso_staging_name"] = "rocky-image-rootdisk"
		var config iso.Config

		_, err := config.Prepare(raw)

		Expect(err).To(MatchError(ContainSubstring("must not collide")))
	})

	It("rejects staging options for an externally managed DataVolume", func() {
		raw := baseRaw()
		raw["iso_volume_name"] = "existing"
		raw["iso_storage_size"] = "12Gi"
		var config iso.Config

		_, err := config.Prepare(raw)

		Expect(err).To(MatchError(ContainSubstring("managed ISO staging options")))
	})
})
