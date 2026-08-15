Type: `kubevirt-iso`
Artifact BuilderId: `kubevirt.iso`

The KubeVirt ISO builder creates a VM image inside a Kubernetes cluster from
installation media. The builder can use an existing CDI DataVolume, import an
HTTP URL through CDI, or upload a local ISO through the Kubernetes API. It
supports Linux and Windows guests. Provisioning is done through SSH or WinRM
once the guest is installed.

---

## Basic Example

Here is a basic example showing how to build a Linux VM image using a Fedora ISO:

```hcl
source "kubevirt-iso" "fedora" {
  # Kubernetes configuration
  kube_config     = "~/.kube/config"
  name            = "fedora-42-rand-85"
  namespace       = "vm-images"
  iso_url          = "https://download.fedoraproject.org/pub/fedora/linux/releases/42/Server/x86_64/iso/Fedora-Server-dvd-x86_64-42-1.1.iso"
  iso_storage_size = "3Gi"

  # Temporary VM type and preferences
  disk_size     = "10Gi"
  instance_type = "o1.medium"
  preference    = "fedora"

  # Timeout for installation to complete
  installation_wait_timeout = "15m"
}

build {
  sources = ["source.kubevirt-iso.fedora"]
}
```

## Installation Media Sources

Exactly one installation-media source must be configured. Both sources resolve to
a single CDI DataVolume that the temporary VM attaches as its CD-ROM; CDI does
all data movement.

### HTTP or HTTPS URL

Use `iso_url` for a URL reachable from inside the cluster. The builder creates a
DataVolume with a CDI HTTP source and CDI imports it — the Packer host never
downloads or re-uploads the ISO.

```hcl
iso_url          = "https://mirror.example.com/rocky.iso"
iso_storage_size = "12Gi"
# iso_checksum   = "sha256:<64-character digest>"
```

The builder keeps this managed DataVolume after the build — on success as well as
on a failed or cancelled run — so a later build reuses the (often multi-GB)
import instead of downloading it again. A retained volume is reused only when its
source URL, checksum, size, and storage class still match; force a fresh import
with `packer build -force`, or remove it with `kubectl -n <ns> delete dv
<name>-iso`. `iso_http_secret_ref` and `iso_http_cert_config_map` can reference
credentials and additional certificate authorities in the build namespace. HTTP
checksum validation is performed by CDI and requires CDI 1.65 or newer; if the
API server prunes the `checksum` field, the builder fails before accepting the
imported media.

### Existing DataVolume

Reference a DataVolume you (or another tool) manage. This is the right choice for
media that already lives in the cluster — for example a local ISO staged in
advance by an external tool.

```hcl
iso_volume_name = "rocky-9-7-install-iso"
```

The builder validates the DataVolume, waits for it to be ready, and **never
deletes** a DataVolume supplied through `iso_volume_name`.

> **Note:** Staging a *local* ISO into the cluster is intentionally out of scope
> for this builder — that is a separate concern best handled before the build
> (e.g. `virtctl image-upload` or a dedicated staging tool). Point
> `iso_volume_name` at the resulting DataVolume.

### Staged media readiness (air-gapped `stage-iso`)

When `iso_volume_name` (or an `extra_media` `data_volume`) points at a DataVolume
produced by `harvester-image stage-iso`, the builder gates readiness on the
`harvester-image-tools/stage-complete` annotation on the volume's bound PVC —
**not** on the CDI phase. A `stage-iso` DataVolume is a blank Block volume that
CDI reports as `Succeeded` (bound) *before* its bytes are raw-populated out of
band, so trusting the phase could boot a blank or half-staged CD-ROM. Any other
DataVolume falls back to ordinary CDI-phase readiness. Optionally set `iso_digest`
(or `extra_media { sha512 = ... }`) to pin the expected SHA-512 against the
`harvester-image-tools/stage-content-sha512` marker and fail closed on a
mismatch. The builder only consumes the volume; it never creates, mutates, or
deletes it.

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

<!-- Code generated from the comments of the ExtraMedia struct in builder/kubevirt/iso/config.go; DO NOT EDIT MANUALLY -->

- `data_volume` (string) - DataVolume is the name of an existing CDI DataVolume in the build namespace
  to attach read-only. It is typically staged out-of-band (e.g. by
  `harvester-image stage-iso`); this builder only attaches it.

<!-- End of code generated from the comments of the ExtraMedia struct in builder/kubevirt/iso/config.go; -->


<!-- Code generated from the comments of the ExtraMedia struct in builder/kubevirt/iso/config.go; DO NOT EDIT MANUALLY -->

- `as` (string) - As is the device kind: "cdrom" (default, read-only) or "disk".

- `name` (string) - Name is the disk device name; a unique name is generated when empty.

- `bus` (string) - Bus is the device bus: "scsi" (default), "sata", "virtio", or "usb".

- `sha512` (string) - SHA512 optionally pins the media content digest. When set and the
  referenced DataVolume was produced by `harvester-image stage-iso`, it must
  equal the volume's harvester-image-tools/stage-content-sha512 marker or the
  build fails closed. Accepts a bare SHA-512 hex digest or a "sha512:"-prefixed one.

<!-- End of code generated from the comments of the ExtraMedia struct in builder/kubevirt/iso/config.go; -->


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

## KubeVirt-ISO Builder Configuration Reference

### Required Configuration

<!-- Code generated from the comments of the Config struct in builder/kubevirt/iso/config.go; DO NOT EDIT MANUALLY -->

- `kube_config` (string) - KubeConfig is the path to the kubeconfig file.

- `name` (string) - Name is the name of the VM image.

- `namespace` (string) - Namespace is the namespace in which to create the VM image.

- `disk_size` (string) - DiskSize is the size of the root disk to of the temporary VM.

- `preference` (string) - Preference is the name of the Preference resource to use in the temporary VM.

- `installation_wait_timeout` (duration string | ex: "1h5m2s") - InstallationWaitTimeout is the amount of time to wait for the installation to be completed.

<!-- End of code generated from the comments of the Config struct in builder/kubevirt/iso/config.go; -->


### Not Required Configuration

<!-- Code generated from the comments of the Config struct in builder/kubevirt/iso/config.go; DO NOT EDIT MANUALLY -->

- `iso_volume_name` (string) - IsoVolumeName is the name of an existing, user-managed DataVolume that
  contains the installation ISO. Exactly one of iso_volume_name or iso_url
  must be set.

- `iso_digest` (string) - IsoDigest optionally pins the installation ISO's content digest when
  iso_volume_name references a DataVolume produced by `harvester-image
  stage-iso`. When set, it must equal the volume's
  harvester-image-tools/stage-content-sha512 marker or the build fails
  closed. Accepts a bare SHA-512 hex digest or a "sha512:"-prefixed one.
  Only valid with iso_volume_name.

- `iso_url` (string) - IsoURL is an HTTP or HTTPS URL that CDI importer pods can reach. The
  builder creates a DataVolume that CDI imports inside the cluster and
  keeps it after the build so later runs reuse the (often multi-GB) import;
  force a fresh import with `packer build -force` or delete the DataVolume.

- `iso_staging_name` (string) - IsoStagingName is the name used for the builder-managed ISO DataVolume
  created for iso_url. Defaults to "<name>-iso".

- `iso_storage_size` (string) - IsoStorageSize is the capacity of the builder-managed ISO DataVolume.
  It is required with iso_url.

- `iso_storage_class` (string) - IsoStorageClass is the optional StorageClass for the managed ISO DataVolume.

- `iso_checksum` (string) - IsoChecksum verifies the imported media. Supported formats are md5:, sha1:,
  sha256:, and sha512: followed by a hex digest. HTTP checksum validation is
  performed by CDI and requires CDI 1.65 or newer.

- `iso_http_secret_ref` (string) - IsoHTTPSecretRef names a Secret containing credentials for an HTTP source.

- `iso_http_cert_config_map` (string) - IsoHTTPCertConfigMap names a ConfigMap containing additional CAs for an
  HTTPS source.

- `iso_staging_timeout` (duration string | ex: "1h5m2s") - IsoStagingTimeout is the maximum time allowed for CDI to import managed
  installation media. Defaults to one hour.

- `instance_type` (string) - InstanceType is the name of the InstanceType resource to use in the temporary VM.
  Mutually exclusive with cpu/memory: set either an instance_type or explicit
  cpu/memory, but not both. KubeVirt forbids combining an instancetype matcher
  with explicit CPU/memory on the VM domain.

- `instance_type_kind` (string) - InstanceTypeKind is the kind of the InstanceType resource to use in the temporary VM.
  Other supported value is "virtualmachineclusterinstancetype".

- `cpu_sockets` (uint32) - CPUSockets is the number of guest CPU sockets to assign to the temporary VM.
  Only used when instance_type is not set. Note that some preferences carry a
  CPU requirement bound to a specific topology dimension (for example the
  windows.11 preference uses preferSockets and requires >= 2 vCPU as sockets),
  so set the dimension the chosen preference expects.

- `cpu_cores` (uint32) - CPUCores is the number of guest CPU cores per socket to assign to the
  temporary VM. Only used when instance_type is not set.

- `cpu_threads` (uint32) - CPUThreads is the number of guest CPU threads per core to assign to the
  temporary VM. Only used when instance_type is not set.

- `memory` (string) - Memory is the amount of guest memory to assign to the temporary VM,
  expressed as a Kubernetes quantity (e.g. "16Gi").
  Only used when instance_type is not set.

- `preference_kind` (string) - PreferenceKind is the kind of the Preference resource to use in the temporary VM.
  Other supported value is "virtualmachineclusterpreference".

- `os_type` (string) - OperatingSystemType is the type of operating system to install.
  Supported values are "linux" and "windows". Default is "linux".

- `disk_interface` (string) - DiskInterface is the bus used by the primary root disk.
  Supported values are "virtio", "sata", "scsi", and "usb".
  If unset, KubeVirt or the selected preference chooses the bus.

- `disk_bus` (string) - DiskBus is the bus type to use for CD-ROM disk devices on the temporary VM.
  Supported values are "scsi", "sata", and "virtio".
  Defaults to "scsi", which is compatible with both x86 and arm64 architectures.
  Use "sata" on x86 clusters if required by your storage configuration.

- `networks` ([]Network) - Networks is a list of networks to attach to the temporary VM.
  If no networks are specified, a single pod network will be used.

- `extra_media` ([]ExtraMedia) - ExtraMedia is a list of additional, read-only media to attach to the
  temporary VM (for example an installer payload staged as a CDI
  DataVolume). Each entry is attached as a read-only CD-ROM by default and
  is never part of the captured image.

- `media_files` ([]string) - MediaFiles is a path list of files to be copied and used during the ISO installation.

- `boot_command` ([]string) - BootCommand is a list of strings that represent the keystrokes to be sent to the VM console
  to automate the installation via a new VNC connection.

- `boot_wait` (duration string | ex: "1h5m2s") - BootWait is the amount of time to wait before sending the boot command.
  This is useful if the VM takes some time to boot and be ready to accept keystrokes.

- `communicator` (string) - Communicator is the type of communicator to use to connect to the VM.
  Supported values are "ssh" and "winrm".

- `ssh_host` (string) - SSHHost is the hostname or IP address to use to connect via SSH.

- `ssh_local_port` (int) - SSHLocalPort is the local port to use to connect via SSH.

- `ssh_remote_port` (int) - SSHRemotePort is the remote port to use to connect via SSH.

- `ssh_username` (string) - SSHUsername is the username to use to connect via SSH.

- `ssh_password` (string) - SSHPassword is the password to use to connect via SSH.

- `ssh_wait_timeout` (duration string | ex: "1h5m2s") - SSHWaitTimeout is the amount of time to wait for the SSH service to be available.

- `winrm_host` (string) - WinRMHost is the hostname or IP address to use to connect via WinRM.

- `winrm_local_port` (int) - WinRMLocalPort is the local port to use to connect via WinRM.

- `winrm_remote_port` (int) - WinRMRemotePort is the remote port to use to connect via WinRM.

- `winrm_username` (string) - WinRMUsername is the username to use to connect via WinRM.

- `winrm_password` (string) - WinRMPassword is the password to use to connect via WinRM.

- `winrm_wait_timeout` (duration string | ex: "1h5m2s") - WinRMWaitTimeout is the amount of time to wait for the WinRM service to be available.

- `keep_vm` (bool) - KeepVM indicates whether to keep the temporary VM after the image has been created.
  If false, the VM and all its resources will be deleted after the image is created.
  If true, the VM and the managed storage resources it still references are retained.
  Default is false.
  
  This can be useful for debugging purposes, to inspect the VM and its disks.
  However, it is recommended to set this to false in production environments to avoid
  resource leaks.

- `skip_create_image` (bool) - SkipCreateImage when set to true skips creating the final bootable volume
  DataSource and does not register the artifact with HCP Packer. This is
  useful for iterative debugging when you do not want to produce a final image.
  Default is false.

<!-- End of code generated from the comments of the Config struct in builder/kubevirt/iso/config.go; -->


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

<!-- Code generated from the comments of the Network struct in builder/kubevirt/iso/config.go; DO NOT EDIT MANUALLY -->

Network represents a network type and a resource that should be connected to the VM.
Source: https://kubevirt.io/api-reference/v1.6.0/definitions.html#_v1_network

<!-- End of code generated from the comments of the Network struct in builder/kubevirt/iso/config.go; -->

<!-- Code generated from the comments of the Network struct in builder/kubevirt/iso/config.go; DO NOT EDIT MANUALLY -->

- `name` (string) - Network name.
  Must be a DNS_LABEL and unique within the VM.
  More info: https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#names

<!-- End of code generated from the comments of the Network struct in builder/kubevirt/iso/config.go; -->


<!-- Code generated from the comments of the NetworkSource struct in builder/kubevirt/iso/config.go; DO NOT EDIT MANUALLY -->

Represents the source resource that will be connected to the VM.
Only one of its members may be specified.

<!-- End of code generated from the comments of the NetworkSource struct in builder/kubevirt/iso/config.go; -->

<!-- Code generated from the comments of the NetworkSource struct in builder/kubevirt/iso/config.go; DO NOT EDIT MANUALLY -->

- `pod` (\*PodNetwork) - Pod

- `multus` (\*MultusNetwork) - Multus

<!-- End of code generated from the comments of the NetworkSource struct in builder/kubevirt/iso/config.go; -->


<!-- Code generated from the comments of the PodNetwork struct in builder/kubevirt/iso/config.go; DO NOT EDIT MANUALLY -->

Represents the stock pod network interface.
Source: https://kubevirt.io/api-reference/v1.6.0/definitions.html#_v1_podnetwork

<!-- End of code generated from the comments of the PodNetwork struct in builder/kubevirt/iso/config.go; -->

<!-- Code generated from the comments of the PodNetwork struct in builder/kubevirt/iso/config.go; DO NOT EDIT MANUALLY -->

- `vmNetworkCIDR` (string) - CIDR for VM network.
  Default 10.0.2.0/24 if not specified.

- `vmIPv6NetworkCIDR` (string) - IPv6 CIDR for the VM network.
  Defaults to fd10:0:2::/120 if not specified.

<!-- End of code generated from the comments of the PodNetwork struct in builder/kubevirt/iso/config.go; -->


<!-- Code generated from the comments of the MultusNetwork struct in builder/kubevirt/iso/config.go; DO NOT EDIT MANUALLY -->

Represents the multus CNI network.
Source: https://kubevirt.io/api-reference/v1.6.0/definitions.html#_v1_multusnetwork

<!-- End of code generated from the comments of the MultusNetwork struct in builder/kubevirt/iso/config.go; -->

<!-- Code generated from the comments of the MultusNetwork struct in builder/kubevirt/iso/config.go; DO NOT EDIT MANUALLY -->

- `networkName` (string) - References to a NetworkAttachmentDefinition CRD object. Format:
  <networkName>, <namespace>/<networkName>. If namespace is not
  specified, VMI namespace is assumed.

- `default` (bool) - Select the default network and add it to the
  multus-cni.io/default-network annotation.

<!-- End of code generated from the comments of the MultusNetwork struct in builder/kubevirt/iso/config.go; -->
