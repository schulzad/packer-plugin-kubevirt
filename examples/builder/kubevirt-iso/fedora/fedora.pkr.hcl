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

source "kubevirt-iso" "fedora" {
  # Kubernetes configuration
  kube_config = var.kube_config
  name        = "fedora-44"
  namespace   = "images"

  # CDI imports this URL from inside the cluster into a DataVolume the plugin
  # manages and cleans up. Use the Server DVD (kickstart-friendly Anaconda
  # installer); the Workstation Live image does not drive an automated ks install.
  # iso_storage_size is the DataVolume capacity and must be >= the ISO (~3.6 GB
  # here); CDI can't size it ahead of the download, so it's required for URL imports.
  iso_url          = "https://download.fedoraproject.org/pub/fedora/linux/releases/44/Server/x86_64/iso/Fedora-Server-dvd-x86_64-44-1.7.iso"
  iso_storage_size = "10Gi"

  # VM sizing and guest profile.
  #
  # Use explicit cpu/memory instead of an instance_type: Harvester rejects the
  # instancetype-only VM, and KubeVirt forbids combining an instance_type with
  # explicit cpu/memory -- so set one path or the other.
  disk_size = "20Gi"
  cpu_cores = 2
  memory    = "4Gi"

  preference      = "fedora"
  preference_kind = "virtualmachineclusterpreference" # or "virtualmachinepreference"
  os_type         = "linux"

  # A pod (masquerade) network works everywhere and is what the SSH port-forward
  # below rides on. Optionally attach a Multus bridge network as a second NIC.
  networks {
    name = "default"
    pod {}
  }
  # networks {
  #   name = "net1"
  #   multus {
  #     networkName = "kube-system/vlan1" # a cluster-specific NetworkAttachmentDefinition
  #   }
  # }

  # Files to include in the ISO installation
  media_files = [
    "./ks.cfg"
  ]

  # Boot process configuration
  # A set of commands to send over VNC connection
  boot_command = [
    "<up>e",                            # Modify GRUB entry
    "<down><down><end>",                # Navigate to kernel line
    " inst.ks=hd:LABEL=OEMDRV:/ks.cfg", # Set kickstart file location
    "<leftCtrlOn>x<leftCtrlOff>"        # Boot with modified command line
  ]
  boot_wait                 = "10s" # Time to wait after boot starts
  installation_wait_timeout = "15m" # Timeout for installation to complete

  # SSH configuration
  communicator     = "ssh"
  ssh_host         = "127.0.0.1"
  ssh_local_port   = 2020
  ssh_remote_port  = 22
  ssh_username     = "user"
  ssh_password     = "root"
  ssh_wait_timeout = "20m"
}

build {
  sources = ["source.kubevirt-iso.fedora"]

  provisioner "shell" {
    inline = [
      "ls -la"
    ]
  }
}
