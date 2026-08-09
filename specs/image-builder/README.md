# Image builder specification

`KubeVirtImageBuilder.smdl` specifies a second builder, `kubevirt-image`, that
builds a golden image **from an existing base image** instead of installing from
an ISO — the KubeVirt/CDI analogue of QEMU's `disk_image = true`.

Key decisions captured in the spec:

- **Layering stays in CDI.** A prior build's output is a CDI `DataSource`; the
  next build clones it into its working root disk via `sourceRef`. No Harvester
  `VirtualMachineImage` is required (that's an optional, downstream publish
  concern, and the fragile `export-from-volume` path this project's tooling
  already works around).
- **Full, independent volumes only.** CDI clone/import produces a standalone PVC;
  there is no qcow2 backing-file chain, so QEMU's `use_backing_file` has no
  equivalent and is intentionally unsupported.
- **Reuses existing plumbing.** The `staging` layer (import + ownership +
  retain-on-failure + cleanup), VM create/stop steps, and the bootable-volume
  finalize step carry over almost wholesale. Net-new work is base-source
  resolution and a cloud-init/sysprep credential disk (replacing kickstart).
- **Local qcow2 files are out of scope**, mirroring the local-ISO decision:
  upload into the cluster first and reference the resulting PVC/DataSource.

This is a design artifact only — no code yet. See the `open_questions` block in
the module for the points to settle before implementation.
