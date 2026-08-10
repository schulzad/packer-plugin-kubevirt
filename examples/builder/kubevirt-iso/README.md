# Example of Packer Templates

- [Fedora](./fedora/)
- [RHEL](./rhel/)
- [Windows](./windows/)

### Example Usage

> 💡 Ensure you are logged in to the Kubernetes cluster and that KubeVirt is installed.

The examples demonstrate both media sources:

- Fedora imports a cluster-reachable HTTP URL with `iso_url` (CDI does the import).
- RHEL and Windows reference an existing DataVolume with `iso_volume_name`.

Change to the directory that contains the relevant files, and then run:

```shell
# Export variable below that is used by the Packer builder
$ export KUBECONFIG=~/.kube/config

# Run the Packer builder
$ packer build ${TEMPLATE_NAME}.pkr.hcl
```

For the existing-DataVolume examples (RHEL, Windows), stage the ISO into the
cluster first — either apply a CDI DataVolume manifest or upload a local ISO with
`virtctl image-upload` — then point `iso_volume_name` at it:

```shell
$ kubectl apply -f rhel-iso.yaml
# or, for a local ISO:
$ virtctl image-upload dv windows-11-x86-64-iso --size=10Gi --image-path=./Windows.iso
```

All referenced Secrets, ConfigMaps, and DataVolumes must be in the configured
build namespace.
