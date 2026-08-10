# Copyright (c) Red Hat, Inc.
# SPDX-License-Identifier: MPL-2.0

packer {
  required_plugins {
    kubevirt = {
      source  = "github.com/hashicorp/kubevirt"
      version = ">= 0.8.0"
    }
  }
}

variable "kube_config" {
  type    = string
  default = "${env("KUBECONFIG")}"
}

# Build a new golden image FROM an existing base image instead of installing
# from an ISO. source_datasource is a prior build's output (here, the DataSource
# produced by the kubevirt-iso fedora example); CDI clones it into the temporary
# VM's root disk, we boot it directly and provision, then capture a new image.
source "kubevirt-image" "fedora" {
  # Kubernetes configuration
  kube_config = var.kube_config
  name        = "fedora-44-golden"
  namespace   = "images"

  # Base image to layer on. It is cloned (a full, independent copy) into the
  # temporary VM's root disk. Defaults to the same namespace unless
  # source_namespace is set.
  source_datasource = "fedora-44"
  # source_namespace = "images"

  # VM sizing. disk_size must be >= the base image's virtual size (fedora-44 was
  # built at 20Gi). Use explicit cpu/memory rather than an instance_type on
  # Harvester (see the kubevirt-iso example for why).
  disk_size = "20Gi"
  cpu_cores = 2
  memory    = "4Gi"

  preference      = "fedora"
  preference_kind = "virtualmachineclusterpreference" # or "virtualmachinepreference"
  os_type         = "linux"

  # A pod (masquerade) network is what the SSH port-forward rides on.
  networks {
    name = "default"
    pod {}
  }

  # No boot_command / media_files / installation_wait_timeout: the disk is
  # already installed, so we boot straight into it and wait for it to be Ready.
  # boot_timeout = "1h"

  # SSH configuration. The base image already carries these credentials from its
  # original kickstart install.
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
    inline = [
      "cat /etc/os-release",
      # Real golden-image work goes here, e.g. installing the guest agent or
      # applying hardening. These need sudo/network, so they are left commented:
      # "sudo dnf install -y qemu-guest-agent",
      # "sudo systemctl enable qemu-guest-agent",
    ]
  }
}
