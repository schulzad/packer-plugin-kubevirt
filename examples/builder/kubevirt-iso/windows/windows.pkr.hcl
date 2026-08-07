# Copyright (c) Red Hat, Inc.
# SPDX-License-Identifier: MPL-2.0

packer {
  required_plugins {
    kubevirt = {
      source  = "github.com/hashicorp/kubevirt"
      version = ">= 0.9.1" # cpu_sockets/cpu_cores/cpu_threads/memory require this fork
    }
  }
}

variable "kube_config" {
  type    = string
  default = "${env("KUBECONFIG")}"
}

source "kubevirt-iso" "windows" {
  # Kubernetes configuration
  kube_config   = var.kube_config
  name          = "windows-11-rand-575"
  namespace     = "images"

  # ISO configuration
  iso_volume_name = "windows-11-x86-64-iso"

  # VM sizing and guest profile.
  #
  # Explicit CPU topology + memory instead of an instance_type: some clusters
  # (e.g. Harvester) reject the instance_type-only VM the plugin used to emit,
  # and KubeVirt forbids combining an instance_type with explicit cpu/memory --
  # so set one path or the other. The windows.11 preference (kept for UEFI +
  # secure boot + TPM 2.0 + bus defaults) uses preferSockets and requires
  # >= 2 vCPU placed on sockets, so cpu_sockets = 2 (not cpu_cores) satisfies it.
  disk_size   = "64Gi"
  cpu_sockets = 2
  cpu_cores   = 1
  cpu_threads = 1
  memory      = "8Gi"

  preference      = "windows.11.virtio"
  preference_kind = "virtualmachineclusterpreference" # or "virtualmachinepreference"
  os_type         = "windows"

  # Files to include in the ISO installation
  media_files = [
    #
    # Note: To avoid License error, set "AcceptEula" to "true" in the "autounattend.xml" file.
    #
    # By setting "AcceptEula" parameter to "true", you are agreeing to the
    # applicable Microsoft end user license agreement(s) for each deployment
    # or installation for the Microsoft product(s).
    #
    "./autounattend.xml",
    "./install-misc.ps1",
    "./set-network.ps1",
    "./enable-winrm.ps1"
  ]

  # Boot process configuration
  # A set of commands to send over VNC connection
  boot_command = [
    "<spacebar><wait>",                # Bypass press any key press challenge
  ]
  boot_wait                 = "5s"     # Time to wait after boot starts
  installation_wait_timeout = "20m"    # Timeout for installation to complete

  # WinRM configuration
  communicator       = "winrm"
  winrm_host         = "127.0.0.1"
  winrm_local_port   = 5000
  winrm_remote_port  = 5985
  winrm_username     = "Administrator"
  winrm_password     = "shadowman"
  winrm_wait_timeout = "25m"
}

build {
  sources = ["source.kubevirt-iso.windows"]

  provisioner "powershell" {
    inline = [
      "Write-Output 'Provisioning started...'",
      "Get-Date",
    ]
  }

  provisioner "windows-shell" {
    inline = [
      "C:\\Windows\\System32\\Sysprep\\sysprep.exe /generalize /oobe /shutdown /mode:vm"
    ]
  }
}
