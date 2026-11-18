# SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
# SPDX-License-Identifier: Apache-2.0

packer {
  required_version = "= 1.16.2"
  required_plugins {
    qemu = {
      version = "= 1.1.8"
      source  = "github.com/hashicorp/qemu"
    }
  }
}

variable "architecture" { type = string }
variable "accelerator" { type = string }
variable "material_manifest" { type = string }
variable "output_directory" { type = string }
variable "build_log_directory" { type = string }
variable "ssh_private_key_file" { type = string }
variable "ssh_public_key_file" { type = string }

source "qemu" "disk" {
  accelerator      = var.accelerator
  communicator     = "none"
  disk_image       = true
  disk_interface   = "ide"
  disk_size        = "8M"
  format           = "qcow2"
  headless         = true
  iso_url          = "${path.root}/seed.raw"
  iso_checksum     = "sha256:${filesha256("${path.root}/seed.raw")}"
  memory           = 128
  cpus             = 1
  output_directory = "${var.output_directory}/build"
  vm_name          = "disk.qcow2"
  boot_wait        = "1ms"
  shutdown_timeout = "30s"
}

build {
  name    = "proof"
  sources = ["source.qemu.disk"]
  post-processor "shell-local" {
    inline = ["bash '${path.root}/verify.sh' '${var.output_directory}/build/disk.qcow2' '${path.root}/seed.raw' '${var.build_log_directory}'"]
  }
}
