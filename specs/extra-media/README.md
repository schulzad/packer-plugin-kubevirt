# Extra build media specification

`KubeVirtExtraMedia.smdl` specifies how the builders attach **additional,
read-only media** to the temporary build VM so provisioners can read large
payloads (for example a ~65MB Cloudbase-Init installer) off a mounted CD-ROM
instead of streaming them into the guest over WinRM/SSH or through a ConfigMap.
It is the KubeVirt/CDI analogue of QEMU's and Proxmox's `cd_files`, adapted to
the fact that KubeVirt can only attach disks that already live in the cluster.

Key decisions captured in the spec:

- **Guest-agnostic.** The plugin attaches a read-only volume and never reads,
  mounts, or interprets the payload — the same boundary as `shutdown_command`.
  What is on the media, and how it is laid out, stays out of plugin code.
- **Reference a published payload (recommended).** The cluster pulls the media —
  an OCI `containerDisk` (the mechanism the Windows builds already use for virtio
  drivers), an existing `DataVolume`/PVC, or a CDI-imported URL — so nothing is
  streamed from the Packer host or through the guest. This matches the existing
  "point at cluster-native media" model (`iso_volume_name`, `source_datasource`)
  and avoids the CDI upload-proxy plumbing we dropped for multi-GB ISOs.
- **Publishing is out of scope.** Building/publishing the payload (e.g. bundling
  the installer into an OCI `containerDisk` image) is a `harvester-image-tools`
  concern; the plugin only consumes a reference.
- **Build-time only.** Extra media is attached read-only to the temporary VM and
  is never part of the captured golden image; `ConfigMap`/`cloudInit`/`Sysprep`
  remain for <1MB config only.
- **`cd_files` is deferred.** A second transport reusing the SDK's `StepCreateCD`
  to build a local ISO and CDI-upload it is captured as a later ergonomic add-on,
  not the first cut.

This is a design artifact only — no code yet. See the `open_questions` block in
the module for the points to settle before implementation.
