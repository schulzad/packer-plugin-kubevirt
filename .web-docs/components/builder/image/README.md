Type: `kubevirt-image`
Artifact BuilderId: `packer.kubevirt.image`

The KubeVirt Image builder creates a golden image inside a Kubernetes cluster
**from an existing base image** instead of installing from an ISO — the
KubeVirt/CDI analogue of the QEMU builder's `disk_image = true`. It clones a CDI
`DataSource` (typically a prior build's output) into the temporary VM's root
disk, boots that disk directly (no ISO, no CD-ROM, no boot command, no
kickstart), provisions over SSH or WinRM, and captures the provisioned disk as a
new `DataSource`. CDI performs all data movement, and every result is a full,
independent volume — there is no qcow2 backing-file chain.

Because layering stays entirely within CDI, build A emits a `DataSource` and
build B clones it: the output of one image build is a valid `source_datasource`
for the next.

---

## Basic Example

This example builds `images/fedora-44-golden` from an existing
`images/fedora-44` DataSource (for example, one produced by the
[`kubevirt-iso`](/packer/integrations/hashicorp/kubevirt/latest/components/builder/iso)
builder):

```hcl
source "kubevirt-image" "fedora" {
  # Kubernetes configuration
  kube_config = "~/.kube/config"
  name        = "fedora-44-golden"
  namespace   = "images"

  # Base image to clone into the temporary VM's root disk.
  source_datasource = "fedora-44"

  # Temporary VM sizing. disk_size must be >= the base image's virtual size.
  disk_size = "20Gi"
  cpu_cores = 2
  memory    = "4Gi"
  preference = "fedora"

  # The base image already carries these credentials.
  communicator     = "ssh"
  ssh_host         = "127.0.0.1"
  ssh_local_port   = 2022
  ssh_remote_port  = 22
  ssh_username     = "user"
  ssh_password     = "root"
  ssh_wait_timeout = "10m"
}

build {
  sources = ["source.kubevirt-image.fedora"]

  provisioner "shell" {
    inline = ["cat /etc/os-release"]
  }
}
```

## Base Image Source

The builder clones an existing CDI `DataSource` into the temporary VM's root
disk. Set `source_datasource` to the DataSource name, and optionally
`source_namespace` if the base lives in a different namespace than the build
(it defaults to `namespace`):

```hcl
source_datasource = "fedora-44"
# source_namespace = "images"
```

The clone is a full, independent copy — the base DataSource is only read, never
modified or deleted. The output `name` must not collide with the base or with
the temporary root disk (`<name>-rootdisk`).

Unlike the ISO builder, there is no installer, so the base image must already be
bootable and reachable by the communicator. Use a base whose credentials you
know (for example a previous `kubevirt-iso` build), or provision first-boot
credentials before connecting.

> **Note:** Today the builder clones an existing in-cluster `DataSource`.
> Additional base sources — an existing PVC, a `VolumeSnapshot`, or a
> CDI-imported disk-image URL — are planned. To build from media that is not yet
> in the cluster, import it first (for example with the `kubevirt-iso` builder or
> an external tool) and point `source_datasource` at the resulting DataSource.

## Extra Media

Attach additional, read-only media to the temporary build VM — for example a
large installer payload — without streaming it into the guest over WinRM/SSH or
squeezing it through a ConfigMap. Each `extra_media` block references an existing
CDI `DataVolume` (staged out-of-band, e.g. by `harvester-image stage-iso`) and
mounts it as a read-only CD-ROM. The media is attached only to the temporary VM
and is never part of the captured image; the builder never assembles or uploads
it — it only attaches what already exists in the cluster.

```hcl
extra_media {
  data_volume = "cloudbase-media-ab12cd34"
  # as   = "cdrom"   # default; or "disk"
  # name = "cloudbase"
  # bus  = "scsi"    # default
}
```

When a `data_volume` was produced by `harvester-image stage-iso`, the builder
gates readiness on the `harvester-image-tools/stage-complete` annotation on the
volume's bound PVC rather than on the CDI phase — a blank Block `stage-iso`
volume reports `Succeeded` (bound) before its bytes are staged, so trusting the
phase could attach a blank or half-staged disk. Any other DataVolume falls back
to ordinary CDI-phase readiness. Optionally set `sha512` on the entry to pin the
expected SHA-512 against the `harvester-image-tools/stage-content-sha512` marker
and fail closed on a mismatch.

<!-- Code generated from the comments of the ExtraMedia struct in builder/kubevirt/image/config.go; DO NOT EDIT MANUALLY -->

- `data_volume` (string) - DataVolume is the name of an existing CDI DataVolume in the build namespace
  to attach read-only. It is typically staged out-of-band (e.g. by
  `harvester-image stage-iso`); this builder only attaches it.

<!-- End of code generated from the comments of the ExtraMedia struct in builder/kubevirt/image/config.go; -->


<!-- Code generated from the comments of the ExtraMedia struct in builder/kubevirt/image/config.go; DO NOT EDIT MANUALLY -->

- `as` (string) - As is the device kind: "cdrom" (default, read-only) or "disk".

- `name` (string) - Name is the disk device name; a unique name is generated when empty.

- `bus` (string) - Bus is the device bus: "scsi" (default), "sata", "virtio", or "usb".

- `sha512` (string) - SHA512 optionally pins the media content digest. When set and the
  referenced DataVolume was produced by `harvester-image stage-iso`, it must
  equal the volume's harvester-image-tools/stage-content-sha512 marker or the
  build fails closed. Accepts a bare SHA-512 hex digest or a "sha512:"-prefixed one.

<!-- End of code generated from the comments of the ExtraMedia struct in builder/kubevirt/image/config.go; -->


## Graceful Shutdown

By default the builder stops the temporary VM through the KubeVirt API once
provisioning completes. If you set `shutdown_command`, the builder instead runs
that command over the communicator and waits up to `shutdown_timeout` for the
guest to power **itself** off before capturing the disk.

The command is opaque to the plugin — it may run `sysprep /generalize`
(Windows), a cloud-init cleanup, or a plain `shutdown` — the builder has no
guest-OS knowledge. Because a guest-initiated power-off is expected, run any
sealing command here rather than in a provisioner: the communicator disconnect
at power-off is handled, not treated as an error.

When `shutdown_command` is set, the temporary VM is created with
`RunStrategy=RerunOnFailure` instead of `Always`, so a clean guest power-off
stays down. (Under `Always`, KubeVirt restarts a VM that powers itself off,
which would, for example, boot a just-generalized Windows image back into OOBE
and re-specialize it.) A crash during provisioning is still retried. Deciding
*what* to run — for example a `seal` template variable that selects a Sysprep
command versus a plain shutdown — is left entirely to your template.

## KubeVirt-Image Builder Configuration Reference

### Required Configuration

<!-- Code generated from the comments of the Config struct in builder/kubevirt/image/config.go; DO NOT EDIT MANUALLY -->

- `kube_config` (string) - KubeConfig is the path to the kubeconfig file.

- `name` (string) - Name is the name of the produced VM image (DataSource).

- `namespace` (string) - Namespace is the namespace in which to create the temporary VM and the
  output image.

- `source_datasource` (string) - SourceDataSource is the name of an existing CDI DataSource to build from —
  typically a prior build's output. Its contents are cloned (a full,
  independent copy) into the temporary VM's root disk.

- `disk_size` (string) - DiskSize is the size of the temporary VM's root disk. It must be at least
  the source image's virtual size.

<!-- End of code generated from the comments of the Config struct in builder/kubevirt/image/config.go; -->


### Not Required Configuration

<!-- Code generated from the comments of the Config struct in builder/kubevirt/image/config.go; DO NOT EDIT MANUALLY -->

- `source_namespace` (string) - SourceNamespace is the namespace of source_datasource. Defaults to namespace.

- `instance_type` (string) - InstanceType is the name of the InstanceType resource to use.
  Mutually exclusive with cpu/memory.

- `instance_type_kind` (string) - InstanceTypeKind is the kind of the InstanceType resource.

- `cpu_sockets` (uint32) - CPUSockets is the guest CPU sockets. Only used when instance_type is unset.

- `cpu_cores` (uint32) - CPUCores is the guest CPU cores per socket. Only used when instance_type is unset.

- `cpu_threads` (uint32) - CPUThreads is the guest CPU threads per core. Only used when instance_type is unset.

- `memory` (string) - Memory is the guest memory (e.g. "4Gi"). Only used when instance_type is unset.

- `preference` (string) - Preference is the name of the Preference resource, recorded on the output.

- `preference_kind` (string) - PreferenceKind is the kind of the Preference resource.

- `os_type` (string) - OperatingSystemType is "linux" or "windows". Default is "linux".

- `disk_interface` (string) - DiskInterface is the bus used by the root disk (virtio, sata, scsi, usb).

- `networks` ([]Network) - Networks is the list of networks to attach. Defaults to a single pod network.

- `extra_media` ([]ExtraMedia) - ExtraMedia is a list of additional, read-only media to attach to the
  temporary VM (for example an installer payload staged as a CDI
  DataVolume). Each entry is attached as a read-only CD-ROM by default and
  is never part of the captured image.

- `communicator` (string) - Communicator is "ssh" or "winrm".

- `ssh_host` (string) - SSHHost is the hostname or IP to connect via SSH.

- `ssh_local_port` (int) - SSHLocalPort is the local port to connect via SSH.

- `ssh_remote_port` (int) - SSHRemotePort is the remote port to connect via SSH.

- `ssh_username` (string) - SSHUsername is the SSH username.

- `ssh_password` (string) - SSHPassword is the SSH password.

- `ssh_wait_timeout` (duration string | ex: "1h5m2s") - SSHWaitTimeout is how long to wait for SSH to be available.

- `winrm_host` (string) - WinRMHost is the hostname or IP to connect via WinRM.

- `winrm_local_port` (int) - WinRMLocalPort is the local port to connect via WinRM.

- `winrm_remote_port` (int) - WinRMRemotePort is the remote port to connect via WinRM.

- `winrm_username` (string) - WinRMUsername is the WinRM username.

- `winrm_password` (string) - WinRMPassword is the WinRM password.

- `winrm_wait_timeout` (duration string | ex: "1h5m2s") - WinRMWaitTimeout is how long to wait for WinRM to be available.

- `boot_timeout` (duration string | ex: "1h5m2s") - BootTimeout is how long to wait for the temporary VM to become Ready.
  Defaults to one hour.

- `keep_vm` (bool) - KeepVM keeps the temporary VM (and the storage it references) after the
  build for debugging.

- `skip_create_image` (bool) - SkipCreateImage skips the final capture and HCP registration.

<!-- End of code generated from the comments of the Config struct in builder/kubevirt/image/config.go; -->


### Shutdown Configuration

<!-- Code generated from the comments of the ShutdownConfig struct in shutdowncommand/config.go; DO NOT EDIT MANUALLY -->

- `shutdown_command` (string) - The command to use to gracefully shut down the machine once all
  provisioning is complete. By default this is an empty string, which
  tells Packer to just forcefully shut down the machine. This setting can
  be safely omitted if for example, a shutdown command to gracefully halt
  the machine is configured inside a provisioning script. If one or more
  scripts require a reboot it is suggested to leave this blank (since
  reboots may fail) and instead specify the final shutdown command in your
  last script.

- `shutdown_timeout` (duration string | ex: "1h5m2s") - The amount of time to wait after executing the shutdown_command for the
  virtual machine to actually shut down. If the machine doesn't shut down
  in this time it is considered an error. By default, the time out is "5m"
  (five minutes).

<!-- End of code generated from the comments of the ShutdownConfig struct in shutdowncommand/config.go; -->


### Network Configuration

<!-- Code generated from the comments of the Network struct in builder/kubevirt/image/config.go; DO NOT EDIT MANUALLY -->

Network mirrors the kubevirt-iso builder's network config so HCL2 generation
stays within this package. It is converted to KubeVirt networks locally.

<!-- End of code generated from the comments of the Network struct in builder/kubevirt/image/config.go; -->

<!-- Code generated from the comments of the Network struct in builder/kubevirt/image/config.go; DO NOT EDIT MANUALLY -->

- `name` (string) - Network name. Must be a DNS_LABEL and unique within the VM.

<!-- End of code generated from the comments of the Network struct in builder/kubevirt/image/config.go; -->


<!-- Code generated from the comments of the NetworkSource struct in builder/kubevirt/image/config.go; DO NOT EDIT MANUALLY -->

NetworkSource selects the network type. Only one member may be specified.

<!-- End of code generated from the comments of the NetworkSource struct in builder/kubevirt/image/config.go; -->

<!-- Code generated from the comments of the NetworkSource struct in builder/kubevirt/image/config.go; DO NOT EDIT MANUALLY -->

- `pod` (\*PodNetwork) - Pod

- `multus` (\*MultusNetwork) - Multus

<!-- End of code generated from the comments of the NetworkSource struct in builder/kubevirt/image/config.go; -->


<!-- Code generated from the comments of the PodNetwork struct in builder/kubevirt/image/config.go; DO NOT EDIT MANUALLY -->

PodNetwork is the stock pod (masquerade) network interface.

<!-- End of code generated from the comments of the PodNetwork struct in builder/kubevirt/image/config.go; -->

<!-- Code generated from the comments of the PodNetwork struct in builder/kubevirt/image/config.go; DO NOT EDIT MANUALLY -->

- `vmNetworkCIDR` (string) - VMNetworkCIDR for the VM network. Default 10.0.2.0/24 if not specified.

- `vmIPv6NetworkCIDR` (string) - VMIPv6NetworkCIDR for the VM network. Defaults to fd10:0:2::/120.

<!-- End of code generated from the comments of the PodNetwork struct in builder/kubevirt/image/config.go; -->


<!-- Code generated from the comments of the MultusNetwork struct in builder/kubevirt/image/config.go; DO NOT EDIT MANUALLY -->

MultusNetwork references a NetworkAttachmentDefinition CRD object.

<!-- End of code generated from the comments of the MultusNetwork struct in builder/kubevirt/image/config.go; -->

<!-- Code generated from the comments of the MultusNetwork struct in builder/kubevirt/image/config.go; DO NOT EDIT MANUALLY -->

- `networkName` (string) - NetworkName references a NetworkAttachmentDefinition: <networkName> or
  <namespace>/<networkName>. If namespace is omitted, the VMI namespace is used.

- `default` (bool) - Default adds the multus-cni.io/default-network annotation.

<!-- End of code generated from the comments of the MultusNetwork struct in builder/kubevirt/image/config.go; -->
