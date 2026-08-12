// Copyright (c) Red Hat, Inc.
// SPDX-License-Identifier: MPL-2.0

//go:generate packer-sdc struct-markdown
//go:generate packer-sdc mapstructure-to-hcl2 -type Config,ExtraMedia,Network,NetworkSource,PodNetwork,MultusNetwork

package iso

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/packer-plugin-sdk/common"
	"github.com/hashicorp/packer-plugin-sdk/shutdowncommand"
	"github.com/hashicorp/packer-plugin-sdk/template/config"
	"github.com/hashicorp/packer-plugin-sdk/template/interpolate"
	"k8s.io/apimachinery/pkg/api/resource"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

// Network represents a network type and a resource that should be connected to the VM.
// Source: https://kubevirt.io/api-reference/v1.6.0/definitions.html#_v1_network
type Network struct {
	// Network name.
	// Must be a DNS_LABEL and unique within the VM.
	// More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#names
	Name string `mapstructure:"name"`

	// NetworkSource represents the network type and the source interface that should be connected to the VM.
	// Defaults to Pod, if no type is specified.
	NetworkSource `mapstructure:",squash"`
}

// Represents the source resource that will be connected to the VM.
// Only one of its members may be specified.
type NetworkSource struct {
	Pod    *PodNetwork    `mapstructure:"pod"`
	Multus *MultusNetwork `mapstructure:"multus"`
}

// Represents the stock pod network interface.
// Source: https://kubevirt.io/api-reference/v1.6.0/definitions.html#_v1_podnetwork
type PodNetwork struct {
	// CIDR for VM network.
	// Default 10.0.2.0/24 if not specified.
	VMNetworkCIDR string `mapstructure:"vmNetworkCIDR,omitempty"`

	// IPv6 CIDR for the VM network.
	// Defaults to fd10:0:2::/120 if not specified.
	VMIPv6NetworkCIDR string `mapstructure:"vmIPv6NetworkCIDR,omitempty"`
}

// Represents the multus CNI network.
// Source: https://kubevirt.io/api-reference/v1.6.0/definitions.html#_v1_multusnetwork
type MultusNetwork struct {
	// References to a NetworkAttachmentDefinition CRD object. Format:
	// <networkName>, <namespace>/<networkName>. If namespace is not
	// specified, VMI namespace is assumed.
	NetworkName string `mapstructure:"networkName"`

	// Select the default network and add it to the
	// multus-cni.io/default-network annotation.
	Default bool `mapstructure:"default,omitempty"`
}

// ExtraMedia is an additional, read-only medium attached to the temporary build
// VM (for example an installer payload staged as a CDI DataVolume). It is never
// part of the captured image.
type ExtraMedia struct {
	// DataVolume is the name of an existing CDI DataVolume in the build namespace
	// to attach read-only. It is typically staged out-of-band (e.g. by
	// `harvester-image stage-iso`); this builder only attaches it.
	DataVolume string `mapstructure:"data_volume" required:"true"`
	// As is the device kind: "cdrom" (default, read-only) or "disk".
	As string `mapstructure:"as" required:"false"`
	// Name is the disk device name; a unique name is generated when empty.
	Name string `mapstructure:"name" required:"false"`
	// Bus is the device bus: "scsi" (default), "sata", "virtio", or "usb".
	Bus string `mapstructure:"bus" required:"false"`
}

type Config struct {
	common.PackerConfig `mapstructure:",squash"`
	// ShutdownConfig provides the same generic shutdown_command and
	// shutdown_timeout contract used by other Packer builders. The command is
	// opaque to this plugin; it may perform any guest preparation before
	// powering off.
	shutdowncommand.ShutdownConfig `mapstructure:",squash"`

	// KubeConfig is the path to the kubeconfig file.
	KubeConfig string `mapstructure:"kube_config" required:"true"`
	// Name is the name of the VM image.
	Name string `mapstructure:"name" required:"true"`
	// Namespace is the namespace in which to create the VM image.
	Namespace string `mapstructure:"namespace" required:"true"`
	// IsoVolumeName is the name of an existing, user-managed DataVolume that
	// contains the installation ISO. Exactly one of iso_volume_name or iso_url
	// must be set.
	IsoVolumeName string `mapstructure:"iso_volume_name" required:"false"`
	// IsoURL is an HTTP or HTTPS URL that CDI importer pods can reach. The
	// builder creates a DataVolume that CDI imports inside the cluster and
	// keeps it after the build so later runs reuse the (often multi-GB) import;
	// force a fresh import with `packer build -force` or delete the DataVolume.
	IsoURL string `mapstructure:"iso_url" required:"false"`
	// IsoStagingName is the name used for the builder-managed ISO DataVolume
	// created for iso_url. Defaults to "<name>-iso".
	IsoStagingName string `mapstructure:"iso_staging_name" required:"false"`
	// IsoStorageSize is the capacity of the builder-managed ISO DataVolume.
	// It is required with iso_url.
	IsoStorageSize string `mapstructure:"iso_storage_size" required:"false"`
	// IsoStorageClass is the optional StorageClass for the managed ISO DataVolume.
	IsoStorageClass string `mapstructure:"iso_storage_class" required:"false"`
	// IsoChecksum verifies the imported media. Supported formats are md5:, sha1:,
	// sha256:, and sha512: followed by a hex digest. HTTP checksum validation is
	// performed by CDI and requires CDI 1.65 or newer.
	IsoChecksum string `mapstructure:"iso_checksum" required:"false"`
	// IsoHTTPSecretRef names a Secret containing credentials for an HTTP source.
	IsoHTTPSecretRef string `mapstructure:"iso_http_secret_ref" required:"false"`
	// IsoHTTPCertConfigMap names a ConfigMap containing additional CAs for an
	// HTTPS source.
	IsoHTTPCertConfigMap string `mapstructure:"iso_http_cert_config_map" required:"false"`
	// IsoStagingTimeout is the maximum time allowed for CDI to import managed
	// installation media. Defaults to one hour.
	IsoStagingTimeout time.Duration `mapstructure:"iso_staging_timeout" required:"false"`
	// DiskSize is the size of the root disk to of the temporary VM.
	DiskSize string `mapstructure:"disk_size" required:"true"`
	// InstanceType is the name of the InstanceType resource to use in the temporary VM.
	// Mutually exclusive with cpu/memory: set either an instance_type or explicit
	// cpu/memory, but not both. KubeVirt forbids combining an instancetype matcher
	// with explicit CPU/memory on the VM domain.
	InstanceType string `mapstructure:"instance_type" required:"false"`
	// InstanceTypeKind is the kind of the InstanceType resource to use in the temporary VM.
	// Other supported value is "virtualmachineclusterinstancetype".
	InstanceTypeKind string `mapstructure:"instance_type_kind" required:"false"`
	// CPUSockets is the number of guest CPU sockets to assign to the temporary VM.
	// Only used when instance_type is not set. Note that some preferences carry a
	// CPU requirement bound to a specific topology dimension (for example the
	// windows.11 preference uses preferSockets and requires >= 2 vCPU as sockets),
	// so set the dimension the chosen preference expects.
	CPUSockets uint32 `mapstructure:"cpu_sockets" required:"false"`
	// CPUCores is the number of guest CPU cores per socket to assign to the
	// temporary VM. Only used when instance_type is not set.
	CPUCores uint32 `mapstructure:"cpu_cores" required:"false"`
	// CPUThreads is the number of guest CPU threads per core to assign to the
	// temporary VM. Only used when instance_type is not set.
	CPUThreads uint32 `mapstructure:"cpu_threads" required:"false"`
	// Memory is the amount of guest memory to assign to the temporary VM,
	// expressed as a Kubernetes quantity (e.g. "16Gi").
	// Only used when instance_type is not set.
	Memory string `mapstructure:"memory" required:"false"`
	// Preference is the name of the Preference resource to use in the temporary VM.
	Preference string `mapstructure:"preference" required:"true"`
	// PreferenceKind is the kind of the Preference resource to use in the temporary VM.
	// Other supported value is "virtualmachineclusterpreference".
	PreferenceKind string `mapstructure:"preference_kind" required:"false"`
	// OperatingSystemType is the type of operating system to install.
	// Supported values are "linux" and "windows". Default is "linux".
	OperatingSystemType string `mapstructure:"os_type" required:"false"`
	// DiskInterface is the bus used by the primary root disk.
	// Supported values are "virtio", "sata", "scsi", and "usb".
	// If unset, KubeVirt or the selected preference chooses the bus.
	DiskInterface string `mapstructure:"disk_interface" required:"false"`
	// DiskBus is the bus type to use for CD-ROM disk devices on the temporary VM.
	// Supported values are "scsi", "sata", and "virtio".
	// Defaults to "scsi", which is compatible with both x86 and arm64 architectures.
	// Use "sata" on x86 clusters if required by your storage configuration.
	DiskBus string `mapstructure:"disk_bus" required:"false"`
	// Networks is a list of networks to attach to the temporary VM.
	// If no networks are specified, a single pod network will be used.
	Networks []Network `mapstructure:"networks" required:"false"`
	// ExtraMedia is a list of additional, read-only media to attach to the
	// temporary VM (for example an installer payload staged as a CDI
	// DataVolume). Each entry is attached as a read-only CD-ROM by default and
	// is never part of the captured image.
	ExtraMedia []ExtraMedia `mapstructure:"extra_media" required:"false"`
	// MediaFiles is a path list of files to be copied and used during the ISO installation.
	MediaFiles []string `mapstructure:"media_files" required:"false"`
	// BootCommand is a list of strings that represent the keystrokes to be sent to the VM console
	// to automate the installation via a new VNC connection.
	BootCommand []string `mapstructure:"boot_command" required:"false"`
	// BootWait is the amount of time to wait before sending the boot command.
	// This is useful if the VM takes some time to boot and be ready to accept keystrokes.
	BootWait time.Duration `mapstructure:"boot_wait" required:"false"`
	// InstallationWaitTimeout is the amount of time to wait for the installation to be completed.
	InstallationWaitTimeout time.Duration `mapstructure:"installation_wait_timeout" required:"true"`
	// Communicator is the type of communicator to use to connect to the VM.
	// Supported values are "ssh" and "winrm".
	Communicator string `mapstructure:"communicator" required:"false"`
	// SSHHost is the hostname or IP address to use to connect via SSH.
	SSHHost string `mapstructure:"ssh_host" required:"false"`
	// SSHLocalPort is the local port to use to connect via SSH.
	SSHLocalPort int `mapstructure:"ssh_local_port" required:"false"`
	// SSHRemotePort is the remote port to use to connect via SSH.
	SSHRemotePort int `mapstructure:"ssh_remote_port" required:"false"`
	// SSHUsername is the username to use to connect via SSH.
	SSHUsername string `mapstructure:"ssh_username" required:"false"`
	// SSHPassword is the password to use to connect via SSH.
	SSHPassword string `mapstructure:"ssh_password" required:"false"`
	// SSHWaitTimeout is the amount of time to wait for the SSH service to be available.
	SSHWaitTimeout time.Duration `mapstructure:"ssh_wait_timeout" required:"false"`
	// WinRMHost is the hostname or IP address to use to connect via WinRM.
	WinRMHost string `mapstructure:"winrm_host" required:"false"`
	// WinRMLocalPort is the local port to use to connect via WinRM.
	WinRMLocalPort int `mapstructure:"winrm_local_port" required:"false"`
	// WinRMRemotePort is the remote port to use to connect via WinRM.
	WinRMRemotePort int `mapstructure:"winrm_remote_port" required:"false"`
	// WinRMUsername is the username to use to connect via WinRM.
	WinRMUsername string `mapstructure:"winrm_username" required:"false"`
	// WinRMPassword is the password to use to connect via WinRM.
	WinRMPassword string `mapstructure:"winrm_password" required:"false"`
	// WinRMWaitTimeout is the amount of time to wait for the WinRM service to be available.
	WinRMWaitTimeout time.Duration `mapstructure:"winrm_wait_timeout" required:"false"`

	// KeepVM indicates whether to keep the temporary VM after the image has been created.
	// If false, the VM and all its resources will be deleted after the image is created.
	// If true, the VM and the managed storage resources it still references are retained.
	// Default is false.
	//
	// This can be useful for debugging purposes, to inspect the VM and its disks.
	// However, it is recommended to set this to false in production environments to avoid
	// resource leaks.
	KeepVM bool `mapstructure:"keep_vm" required:"false"`

	// SkipCreateImage when set to true skips creating the final bootable volume
	// DataSource and does not register the artifact with HCP Packer. This is
	// useful for iterative debugging when you do not want to produce a final image.
	// Default is false.
	SkipCreateImage bool `mapstructure:"skip_create_image" required:"false"`
}

func (c *Config) Prepare(raws ...interface{}) ([]string, error) {
	err := config.Decode(c, &config.DecodeOpts{
		PluginType:  "builder.kubevirt.iso",
		Interpolate: true,
	}, raws...)
	if err != nil {
		return nil, err
	}
	var warnings []string
	if errs := c.ShutdownConfig.Prepare(interpolate.NewContext()); len(errs) != 0 {
		return nil, errs[0]
	}
	if c.ShutdownTimeout < 0 {
		return nil, fmt.Errorf("shutdown_timeout must be greater than zero")
	}

	if c.DiskBus == "" {
		c.DiskBus = "scsi"
	}

	sourceCount := 0
	for _, source := range []string{c.IsoVolumeName, c.IsoURL} {
		if strings.TrimSpace(source) != "" {
			sourceCount++
		}
	}
	if sourceCount != 1 {
		return nil, fmt.Errorf("exactly one of iso_volume_name or iso_url must be set")
	}
	if c.IsoVolumeName == c.Name || c.IsoVolumeName == c.Name+"-rootdisk" {
		return nil, fmt.Errorf("iso_volume_name must not collide with the output or temporary root-disk name")
	}

	if c.IsoURL == "" {
		if c.IsoStagingName != "" || c.IsoStorageSize != "" || c.IsoStorageClass != "" ||
			c.IsoChecksum != "" || c.IsoHTTPSecretRef != "" ||
			c.IsoHTTPCertConfigMap != "" || c.IsoStagingTimeout != 0 {
			return nil, fmt.Errorf("managed ISO staging options cannot be combined with iso_volume_name")
		}
	} else {
		if c.IsoStagingName == "" {
			c.IsoStagingName = c.Name + "-iso"
		}
		if errs := k8svalidation.IsDNS1123Subdomain(c.IsoStagingName); len(errs) != 0 {
			return nil, fmt.Errorf("iso_staging_name %q is invalid: %s", c.IsoStagingName, strings.Join(errs, ", "))
		}
		if c.IsoStagingName == c.Name || c.IsoStagingName == c.Name+"-rootdisk" {
			return nil, fmt.Errorf("iso_staging_name must not collide with the output or temporary root-disk name")
		}
		if c.IsoStagingTimeout == 0 {
			c.IsoStagingTimeout = time.Hour
		}
		if c.IsoStagingTimeout < 0 {
			return nil, fmt.Errorf("iso_staging_timeout must be greater than zero")
		}

		parsedURL, parseErr := url.Parse(c.IsoURL)
		if parseErr != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
			return nil, fmt.Errorf("iso_url must be a valid HTTP or HTTPS URL")
		}
		if c.IsoStorageSize == "" {
			return nil, fmt.Errorf("iso_storage_size must be set with iso_url")
		}
		size, sizeErr := resource.ParseQuantity(c.IsoStorageSize)
		if sizeErr != nil || size.Sign() <= 0 {
			return nil, fmt.Errorf("iso_storage_size must be a positive Kubernetes quantity")
		}
		if c.IsoChecksum == "" {
			warnings = append(warnings, "iso_url is configured without iso_checksum; remote media integrity will not be verified by CDI")
		}
	}
	if c.IsoChecksum != "" {
		algorithm, digest, found := strings.Cut(strings.ToLower(c.IsoChecksum), ":")
		lengths := map[string]int{"md5": 16, "sha1": 20, "sha256": 32, "sha512": 64}
		expectedLength, supported := lengths[algorithm]
		decoded, decodeErr := hex.DecodeString(digest)
		if !found || !supported || decodeErr != nil || len(decoded) != expectedLength {
			return nil, fmt.Errorf("iso_checksum must be md5:, sha1:, sha256:, or sha512: followed by a valid hex digest")
		}
		c.IsoChecksum = algorithm + ":" + strings.ToLower(digest)
	}

	switch c.DiskInterface {
	case "", "virtio", "sata", "scsi", "usb":
	default:
		return nil, fmt.Errorf("disk_interface must be one of virtio, sata, scsi, or usb")
	}

	// Sizing can come either from an instancetype OR from explicit cpu/memory,
	// but never both: KubeVirt rejects a VM that carries both an instancetype
	// matcher and explicit CPU/memory on its domain.
	hasExplicitSizing := c.CPUSockets > 0 || c.CPUCores > 0 || c.CPUThreads > 0 || c.Memory != ""
	if c.InstanceType != "" && hasExplicitSizing {
		return nil, fmt.Errorf("instance_type cannot be combined with cpu_sockets/cpu_cores/cpu_threads/memory; set either instance_type or explicit cpu/memory")
	}
	if c.InstanceType == "" && c.Memory == "" {
		return nil, fmt.Errorf("either instance_type or memory (with optional cpu_sockets/cpu_cores/cpu_threads) must be set")
	}
	if strings.TrimSpace(c.ShutdownCommand) != "" &&
		c.Communicator != "ssh" && c.Communicator != "winrm" {
		return nil, fmt.Errorf("shutdown_command requires an ssh or winrm communicator")
	}

	for _, n := range c.Networks {
		if n.Pod != nil && n.Multus != nil {
			return nil, fmt.Errorf("network %q: only one of pod or multus can be defined", n.Name)
		}
	}

	if err := validateExtraMedia(c.ExtraMedia); err != nil {
		return nil, err
	}
	return warnings, nil
}

func validateExtraMedia(items []ExtraMedia) error {
	reserved := map[string]bool{"rootdisk": true, "cdrom": true, "oemdrv": true, "sysprep": true, "virtiocontainerdisk": true}
	seen := map[string]bool{}
	for i := range items {
		m := items[i]
		if strings.TrimSpace(m.DataVolume) == "" {
			return fmt.Errorf("extra_media[%d]: data_volume must be set", i)
		}
		switch m.As {
		case "", "cdrom", "disk":
		default:
			return fmt.Errorf("extra_media[%d]: as must be cdrom or disk", i)
		}
		switch m.Bus {
		case "", "scsi", "sata", "virtio", "usb":
		default:
			return fmt.Errorf("extra_media[%d]: bus must be scsi, sata, virtio, or usb", i)
		}
		if m.Name != "" {
			if reserved[m.Name] {
				return fmt.Errorf("extra_media[%d]: name %q collides with a reserved disk name", i, m.Name)
			}
			if seen[m.Name] {
				return fmt.Errorf("extra_media[%d]: duplicate name %q", i, m.Name)
			}
			seen[m.Name] = true
		}
	}
	return nil
}
