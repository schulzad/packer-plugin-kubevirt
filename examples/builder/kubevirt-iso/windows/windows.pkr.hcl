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

# `seal` is a TEMPLATE concept, not a plugin setting. It only selects which
# opaque command is handed to the builder's generic shutdown_command below; the
# plugin knows nothing about Sysprep or generalization. Set -var seal=false to
# power off without generalizing (handy for validating a build by hand), then
# rebuild with seal=true (default) to produce the clone-ready image.
variable "seal" {
  type    = bool
  default = true
}

source "kubevirt-iso" "windows" {
  # Kubernetes configuration
  kube_config = var.kube_config
  name        = "windows-11-rand-575"
  namespace   = "images"

  # Windows install media generally isn't available at a public URL, so stage it
  # into the cluster first (e.g. `virtctl image-upload`) and reference the
  # resulting DataVolume by name.
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
    "<spacebar><wait>", # Bypass press any key press challenge
  ]
  boot_wait                 = "5s"  # Time to wait after boot starts
  installation_wait_timeout = "20m" # Timeout for installation to complete

  # WinRM configuration
  communicator       = "winrm"
  winrm_host         = "127.0.0.1"
  winrm_local_port   = 5000
  winrm_remote_port  = 5985
  winrm_username     = "Administrator"
  winrm_password     = "shadowman"
  winrm_wait_timeout = "25m"

  # Final guest-initiated power-off, run after provisioning over the communicator.
  # The builder just runs this command and waits (up to shutdown_timeout) for the
  # guest to power itself off before capturing the disk; because a command is set,
  # the temporary VM runs with RunStrategy=RerunOnFailure so a clean power-off
  # stays down instead of being auto-restarted. Sysprep /shutdown belongs here (not
  # in a provisioner) so the WinRM disconnect at power-off is expected, not an error.
  shutdown_command = var.seal ? "C:\\Windows\\System32\\Sysprep\\sysprep.exe /generalize /oobe /shutdown /mode:vm" : "shutdown /s /t 10 /f /c \"build complete (unsealed)\""
  shutdown_timeout = "30m"
}

build {
  sources = ["source.kubevirt-iso.windows"]

  provisioner "powershell" {
    inline = [
      "Write-Output 'Provisioning started...'",
      "Get-Date",
    ]
  }

  # Note: the Sysprep /generalize seal is the final power-off and is issued via
  # shutdown_command (above), not a provisioner, so the WinRM disconnect when the
  # guest powers off is handled rather than treated as a failure.
}
