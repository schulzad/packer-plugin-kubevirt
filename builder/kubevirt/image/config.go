// Copyright (c) Red Hat, Inc.
// SPDX-License-Identifier: MPL-2.0

//go:generate packer-sdc struct-markdown
//go:generate packer-sdc mapstructure-to-hcl2 -type Config,ExtraMedia,Network,NetworkSource,PodNetwork,MultusNetwork

// Package image implements the kubevirt-image builder, which builds a golden
// image FROM an existing base image instead of installing from an ISO. It clones
// a prior build's CDI DataSource (or another in-cluster volume) into the
// temporary VM's root disk, boots it directly, provisions over SSH/WinRM, and
// captures the result as a new DataSource — the KubeVirt/CDI analogue of QEMU's
// disk_image=true. It deliberately reuses the kubevirt-iso builder's internals
// (validation, port-forward, stop, and bootable-volume finalize steps); only the
// boot-from-disk VM creation is new here.
package image

import (
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/packer-plugin-sdk/common"
	"github.com/hashicorp/packer-plugin-sdk/shutdowncommand"
	"github.com/hashicorp/packer-plugin-sdk/template/config"
	"github.com/hashicorp/packer-plugin-sdk/template/interpolate"
	"k8s.io/apimachinery/pkg/api/resource"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

// Network mirrors the kubevirt-iso builder's network config so HCL2 generation
// stays within this package. It is converted to KubeVirt networks locally.
type Network struct {
	// Network name. Must be a DNS_LABEL and unique within the VM.
	Name string `mapstructure:"name"`
	// NetworkSource is the network type connected to the VM; defaults to Pod.
	NetworkSource `mapstructure:",squash"`
}

// NetworkSource selects the network type. Only one member may be specified.
type NetworkSource struct {
	Pod    *PodNetwork    `mapstructure:"pod"`
	Multus *MultusNetwork `mapstructure:"multus"`
}

// PodNetwork is the stock pod (masquerade) network interface.
type PodNetwork struct {
	// VMNetworkCIDR for the VM network. Default 10.0.2.0/24 if not specified.
	VMNetworkCIDR string `mapstructure:"vmNetworkCIDR,omitempty"`
	// VMIPv6NetworkCIDR for the VM network. Defaults to fd10:0:2::/120.
	VMIPv6NetworkCIDR string `mapstructure:"vmIPv6NetworkCIDR,omitempty"`
}

// MultusNetwork references a NetworkAttachmentDefinition CRD object.
type MultusNetwork struct {
	// NetworkName references a NetworkAttachmentDefinition: <networkName> or
	// <namespace>/<networkName>. If namespace is omitted, the VMI namespace is used.
	NetworkName string `mapstructure:"networkName"`
	// Default adds the multus-cni.io/default-network annotation.
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
	// Name is the name of the produced VM image (DataSource).
	Name string `mapstructure:"name" required:"true"`
	// Namespace is the namespace in which to create the temporary VM and the
	// output image.
	Namespace string `mapstructure:"namespace" required:"true"`

	// SourceDataSource is the name of an existing CDI DataSource to build from —
	// typically a prior build's output. Its contents are cloned (a full,
	// independent copy) into the temporary VM's root disk.
	SourceDataSource string `mapstructure:"source_datasource" required:"true"`
	// SourceNamespace is the namespace of source_datasource. Defaults to namespace.
	SourceNamespace string `mapstructure:"source_namespace" required:"false"`

	// DiskSize is the size of the temporary VM's root disk. It must be at least
	// the source image's virtual size.
	DiskSize string `mapstructure:"disk_size" required:"true"`

	// InstanceType is the name of the InstanceType resource to use.
	// Mutually exclusive with cpu/memory.
	InstanceType string `mapstructure:"instance_type" required:"false"`
	// InstanceTypeKind is the kind of the InstanceType resource.
	InstanceTypeKind string `mapstructure:"instance_type_kind" required:"false"`
	// CPUSockets is the guest CPU sockets. Only used when instance_type is unset.
	CPUSockets uint32 `mapstructure:"cpu_sockets" required:"false"`
	// CPUCores is the guest CPU cores per socket. Only used when instance_type is unset.
	CPUCores uint32 `mapstructure:"cpu_cores" required:"false"`
	// CPUThreads is the guest CPU threads per core. Only used when instance_type is unset.
	CPUThreads uint32 `mapstructure:"cpu_threads" required:"false"`
	// Memory is the guest memory (e.g. "4Gi"). Only used when instance_type is unset.
	Memory string `mapstructure:"memory" required:"false"`
	// Preference is the name of the Preference resource, recorded on the output.
	Preference string `mapstructure:"preference" required:"false"`
	// PreferenceKind is the kind of the Preference resource.
	PreferenceKind string `mapstructure:"preference_kind" required:"false"`
	// OperatingSystemType is "linux" or "windows". Default is "linux".
	OperatingSystemType string `mapstructure:"os_type" required:"false"`
	// DiskInterface is the bus used by the root disk (virtio, sata, scsi, usb).
	DiskInterface string `mapstructure:"disk_interface" required:"false"`
	// Networks is the list of networks to attach. Defaults to a single pod network.
	Networks []Network `mapstructure:"networks" required:"false"`
	// ExtraMedia is a list of additional, read-only media to attach to the
	// temporary VM (for example an installer payload staged as a CDI
	// DataVolume). Each entry is attached as a read-only CD-ROM by default and
	// is never part of the captured image.
	ExtraMedia []ExtraMedia `mapstructure:"extra_media" required:"false"`

	// Communicator is "ssh" or "winrm".
	Communicator string `mapstructure:"communicator" required:"false"`
	// SSHHost is the hostname or IP to connect via SSH.
	SSHHost string `mapstructure:"ssh_host" required:"false"`
	// SSHLocalPort is the local port to connect via SSH.
	SSHLocalPort int `mapstructure:"ssh_local_port" required:"false"`
	// SSHRemotePort is the remote port to connect via SSH.
	SSHRemotePort int `mapstructure:"ssh_remote_port" required:"false"`
	// SSHUsername is the SSH username.
	SSHUsername string `mapstructure:"ssh_username" required:"false"`
	// SSHPassword is the SSH password.
	SSHPassword string `mapstructure:"ssh_password" required:"false"`
	// SSHWaitTimeout is how long to wait for SSH to be available.
	SSHWaitTimeout time.Duration `mapstructure:"ssh_wait_timeout" required:"false"`
	// WinRMHost is the hostname or IP to connect via WinRM.
	WinRMHost string `mapstructure:"winrm_host" required:"false"`
	// WinRMLocalPort is the local port to connect via WinRM.
	WinRMLocalPort int `mapstructure:"winrm_local_port" required:"false"`
	// WinRMRemotePort is the remote port to connect via WinRM.
	WinRMRemotePort int `mapstructure:"winrm_remote_port" required:"false"`
	// WinRMUsername is the WinRM username.
	WinRMUsername string `mapstructure:"winrm_username" required:"false"`
	// WinRMPassword is the WinRM password.
	WinRMPassword string `mapstructure:"winrm_password" required:"false"`
	// WinRMWaitTimeout is how long to wait for WinRM to be available.
	WinRMWaitTimeout time.Duration `mapstructure:"winrm_wait_timeout" required:"false"`

	// BootTimeout is how long to wait for the temporary VM to become Ready.
	// Defaults to one hour.
	BootTimeout time.Duration `mapstructure:"boot_timeout" required:"false"`

	// KeepVM keeps the temporary VM (and the storage it references) after the
	// build for debugging.
	KeepVM bool `mapstructure:"keep_vm" required:"false"`
	// SkipCreateImage skips the final capture and HCP registration.
	SkipCreateImage bool `mapstructure:"skip_create_image" required:"false"`
}

func (c *Config) Prepare(raws ...interface{}) ([]string, error) {
	if err := config.Decode(c, &config.DecodeOpts{
		PluginType:  "builder.kubevirt.image",
		Interpolate: true,
	}, raws...); err != nil {
		return nil, err
	}
	if errs := c.ShutdownConfig.Prepare(interpolate.NewContext()); len(errs) != 0 {
		return nil, errs[0]
	}
	if c.ShutdownTimeout < 0 {
		return nil, fmt.Errorf("shutdown_timeout must be greater than zero")
	}

	if c.OperatingSystemType == "" {
		c.OperatingSystemType = "linux"
	}
	if c.OperatingSystemType != "linux" && c.OperatingSystemType != "windows" {
		return nil, fmt.Errorf("os_type must be either linux or windows")
	}
	if c.SourceNamespace == "" {
		c.SourceNamespace = c.Namespace
	}
	if c.BootTimeout == 0 {
		c.BootTimeout = time.Hour
	}
	if c.BootTimeout < 0 {
		return nil, fmt.Errorf("boot_timeout must be greater than zero")
	}

	if strings.TrimSpace(c.SourceDataSource) == "" {
		return nil, fmt.Errorf("source_datasource must be set")
	}
	if c.SourceDataSource == c.Name || c.SourceDataSource == c.Name+"-rootdisk" {
		return nil, fmt.Errorf("source_datasource must not collide with the output or temporary root-disk name")
	}
	if c.DiskSize == "" {
		return nil, fmt.Errorf("disk_size must be set")
	}
	if size, err := resource.ParseQuantity(c.DiskSize); err != nil || size.Sign() <= 0 {
		return nil, fmt.Errorf("disk_size must be a positive Kubernetes quantity")
	}

	switch c.DiskInterface {
	case "", "virtio", "sata", "scsi", "usb":
	default:
		return nil, fmt.Errorf("disk_interface must be one of virtio, sata, scsi, or usb")
	}

	// Sizing comes from an instancetype OR explicit cpu/memory, never both.
	hasExplicitSizing := c.CPUSockets > 0 || c.CPUCores > 0 || c.CPUThreads > 0 || c.Memory != ""
	if c.InstanceType != "" && hasExplicitSizing {
		return nil, fmt.Errorf("instance_type cannot be combined with cpu_sockets/cpu_cores/cpu_threads/memory; set either instance_type or explicit cpu/memory")
	}
	if c.InstanceType == "" && c.Memory == "" {
		return nil, fmt.Errorf("either instance_type or memory (with optional cpu_sockets/cpu_cores/cpu_threads) must be set")
	}

	switch c.Communicator {
	case "", "ssh", "winrm":
	default:
		return nil, fmt.Errorf("communicator must be either ssh or winrm")
	}
	if strings.TrimSpace(c.ShutdownCommand) != "" &&
		c.Communicator != "ssh" && c.Communicator != "winrm" {
		return nil, fmt.Errorf("shutdown_command requires an ssh or winrm communicator")
	}

	if c.Name != "" {
		if errs := k8svalidation.IsDNS1123Subdomain(c.Name); len(errs) != 0 {
			return nil, fmt.Errorf("name %q is invalid: %s", c.Name, strings.Join(errs, ", "))
		}
	}

	for _, n := range c.Networks {
		if n.Pod != nil && n.Multus != nil {
			return nil, fmt.Errorf("network %q: only one of pod or multus can be defined", n.Name)
		}
	}

	if err := validateExtraMedia(c.ExtraMedia); err != nil {
		return nil, err
	}

	return nil, nil
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
