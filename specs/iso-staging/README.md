# ISO staging specifications

This directory describes how the `kubevirt-iso` builder resolves installation
media into a single CDI DataVolume.

- `KubeVirtISOStaging.smdl` defines source selection (existing DataVolume or CDI
  HTTP import), DataVolume ownership, readiness, retention, and hand-off to the
  builder.

The builder leans on the CDI primitive: CDI performs all data movement. Staging a
local ISO from the Packer host into the cluster is intentionally out of scope and
left to an external tool (e.g. `virtctl image-upload`) that produces a DataVolume
the builder then consumes via `iso_volume_name`. See the `out_of_scope` note in
the module for the rationale and the path to revisiting it later.
