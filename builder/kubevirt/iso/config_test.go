// Copyright IBM Corp. 2013, 2026
// SPDX-License-Identifier: MPL-2.0

package iso_test

import (
	"strings"
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

	It("rejects a shutdown_command without a communicator", func() {
		raw := baseRaw()
		raw["iso_volume_name"] = "existing"
		raw["shutdown_command"] = "shutdown /s /t 10"
		var config iso.Config

		_, err := config.Prepare(raw)

		Expect(err).To(MatchError(ContainSubstring("shutdown_command requires")))
	})

	It("defaults shutdown_timeout when a shutdown_command is set", func() {
		raw := baseRaw()
		raw["iso_volume_name"] = "existing"
		raw["communicator"] = "winrm"
		raw["shutdown_command"] = "shutdown /s /t 10"
		var config iso.Config

		_, err := config.Prepare(raw)

		Expect(err).NotTo(HaveOccurred())
		Expect(config.ShutdownTimeout).To(Equal(5 * time.Minute))
	})

	It("accepts extra_media referencing a DataVolume", func() {
		raw := baseRaw()
		raw["iso_volume_name"] = "existing"
		raw["extra_media"] = []map[string]any{{"data_volume": "cloudbase-media"}}
		var config iso.Config

		_, err := config.Prepare(raw)

		Expect(err).NotTo(HaveOccurred())
		Expect(config.ExtraMedia).To(HaveLen(1))
		Expect(config.ExtraMedia[0].DataVolume).To(Equal("cloudbase-media"))
	})

	It("rejects extra_media without a data_volume", func() {
		raw := baseRaw()
		raw["iso_volume_name"] = "existing"
		raw["extra_media"] = []map[string]any{{"as": "cdrom"}}
		var config iso.Config

		_, err := config.Prepare(raw)

		Expect(err).To(MatchError(ContainSubstring("data_volume")))
	})

	It("rejects an extra_media name that collides with a builder disk", func() {
		raw := baseRaw()
		raw["iso_volume_name"] = "existing"
		raw["extra_media"] = []map[string]any{{"data_volume": "m", "name": "cdrom"}}
		var config iso.Config

		_, err := config.Prepare(raw)

		Expect(err).To(MatchError(ContainSubstring("reserved")))
	})

	It("normalizes an iso_digest pin on an existing DataVolume", func() {
		raw := baseRaw()
		raw["iso_volume_name"] = "existing"
		raw["iso_digest"] = "SHA512:" + strings.ToUpper(sampleSHA512)
		var config iso.Config

		_, err := config.Prepare(raw)

		Expect(err).NotTo(HaveOccurred())
		Expect(config.IsoDigest).To(Equal(sampleSHA512))
	})

	It("rejects an iso_digest without iso_volume_name", func() {
		raw := baseRaw()
		raw["iso_url"] = "https://mirror.example.test/rocky.iso"
		raw["iso_storage_size"] = "12Gi"
		raw["iso_digest"] = sampleSHA512
		var config iso.Config

		_, err := config.Prepare(raw)

		Expect(err).To(MatchError(ContainSubstring("iso_digest is only valid with iso_volume_name")))
	})

	It("rejects a malformed iso_digest", func() {
		raw := baseRaw()
		raw["iso_volume_name"] = "existing"
		raw["iso_digest"] = "not-a-digest"
		var config iso.Config

		_, err := config.Prepare(raw)

		Expect(err).To(MatchError(ContainSubstring("iso_digest")))
	})

	It("accepts and normalizes an extra_media sha512 pin", func() {
		raw := baseRaw()
		raw["iso_volume_name"] = "existing"
		raw["extra_media"] = []map[string]any{{"data_volume": "m", "sha512": strings.ToUpper(sampleSHA512)}}
		var config iso.Config

		_, err := config.Prepare(raw)

		Expect(err).NotTo(HaveOccurred())
		Expect(config.ExtraMedia[0].SHA512).To(Equal(sampleSHA512))
	})

	It("rejects a malformed extra_media sha512", func() {
		raw := baseRaw()
		raw["iso_volume_name"] = "existing"
		raw["extra_media"] = []map[string]any{{"data_volume": "m", "sha512": "deadbeef"}}
		var config iso.Config

		_, err := config.Prepare(raw)

		Expect(err).To(MatchError(ContainSubstring("sha512")))
	})
})

const sampleSHA512 = "cf83e1357eefb8bdf1542850d66d8007d620e4050b5715dc83f4a921d36ce9ce" +
	"47d0d13c5d85f2b0ff8318d2877eec2f63b931bd47417a81a538327af927da3e"
