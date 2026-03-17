package db

// SeedDefaultTemplates inserts built-in templates when the templates table is empty.
// Safe to call on every startup — does nothing if templates already exist.
func (d *DB) SeedDefaultTemplates() error {
	var count int
	if err := d.db.QueryRow(`SELECT COUNT(*) FROM templates`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	for _, t := range defaultTemplates {
		if _, err := d.CreateTemplate(t.name, t.description, t.hcl, ""); err != nil {
			return err
		}
	}
	return nil
}

type seedTemplate struct {
	name        string
	description string
	hcl         string
}

var defaultTemplates = []seedTemplate{
	{
		name:        "alpine-pod",
		description: "Minimal Alpine Linux pod with terminal access",
		hcl: `terraform {
  required_providers {
    kubernetes = { source = "hashicorp/kubernetes" }
  }
}

variable "transition"   { default = "start" }
variable "name"         { default = "alpine-pod" }
variable "namespace"    { default = "default" }
variable "image"        { default = "alpine:3.20" }
variable "agent_token"  { default = "" }
variable "tahini_url"   { default = "" }

provider "kubernetes" {}

resource "kubernetes_pod_v1" "main" {
  count = var.transition == "stop" ? 0 : 1
  metadata {
    name      = var.name
    namespace = var.namespace
  }
  spec {
    init_container {
      name              = "tahini-agent-init"
      image             = "tahini-agent:latest"
      image_pull_policy = "IfNotPresent"
      command           = ["cp", "/tahini-agent", "/agent-bin/tahini-agent"]
      volume_mount {
        name       = "agent-bin"
        mount_path = "/agent-bin"
      }
    }
    container {
      name    = "main"
      image   = var.image
      command = ["sh", "-c", "/agent-bin/tahini-agent & sleep infinity"]
      env {
        name  = "TAHINI_URL"
        value = var.tahini_url
      }
      env {
        name  = "TAHINI_AGENT_TOKEN"
        value = var.agent_token
      }
      volume_mount {
        name       = "agent-bin"
        mount_path = "/agent-bin"
      }
    }
    volume {
      name = "agent-bin"
      empty_dir {}
    }
    restart_policy = "Never"
  }
}
`,
	},
	{
		name:        "ubuntu-pod",
		description: "Ubuntu 22.04 interactive pod with terminal access",
		hcl: `terraform {
  required_providers {
    kubernetes = { source = "hashicorp/kubernetes" }
  }
}

variable "transition"   { default = "start" }
variable "name"         { default = "ubuntu-pod" }
variable "namespace"    { default = "default" }
variable "agent_token"  { default = "" }
variable "tahini_url"   { default = "" }

provider "kubernetes" {}

resource "kubernetes_pod_v1" "main" {
  count = var.transition == "stop" ? 0 : 1
  metadata {
    name      = var.name
    namespace = var.namespace
  }
  spec {
    init_container {
      name              = "tahini-agent-init"
      image             = "tahini-agent:latest"
      image_pull_policy = "IfNotPresent"
      command           = ["cp", "/tahini-agent", "/agent-bin/tahini-agent"]
      volume_mount {
        name       = "agent-bin"
        mount_path = "/agent-bin"
      }
    }
    container {
      name    = "main"
      image   = "ubuntu:22.04"
      command = ["sh", "-c", "/agent-bin/tahini-agent & sleep infinity"]
      env {
        name  = "TAHINI_URL"
        value = var.tahini_url
      }
      env {
        name  = "TAHINI_AGENT_TOKEN"
        value = var.agent_token
      }
      volume_mount {
        name       = "agent-bin"
        mount_path = "/agent-bin"
      }
    }
    volume {
      name = "agent-bin"
      empty_dir {}
    }
    restart_policy = "Never"
  }
}
`,
	},
	{
		name:        "python-pod",
		description: "Python 3.12 pod with terminal access",
		hcl: `terraform {
  required_providers {
    kubernetes = { source = "hashicorp/kubernetes" }
  }
}

variable "transition"      { default = "start" }
variable "name"            { default = "python-pod" }
variable "namespace"       { default = "default" }
variable "python_version"  { default = "3.12-slim" }
variable "agent_token"     { default = "" }
variable "tahini_url"      { default = "" }

provider "kubernetes" {}

resource "kubernetes_pod_v1" "main" {
  count = var.transition == "stop" ? 0 : 1
  metadata {
    name      = var.name
    namespace = var.namespace
  }
  spec {
    init_container {
      name              = "tahini-agent-init"
      image             = "tahini-agent:latest"
      image_pull_policy = "IfNotPresent"
      command           = ["cp", "/tahini-agent", "/agent-bin/tahini-agent"]
      volume_mount {
        name       = "agent-bin"
        mount_path = "/agent-bin"
      }
    }
    container {
      name    = "main"
      image   = "python:${var.python_version}"
      command = ["sh", "-c", "/agent-bin/tahini-agent & sleep infinity"]
      env {
        name  = "TAHINI_URL"
        value = var.tahini_url
      }
      env {
        name  = "TAHINI_AGENT_TOKEN"
        value = var.agent_token
      }
      volume_mount {
        name       = "agent-bin"
        mount_path = "/agent-bin"
      }
    }
    volume {
      name = "agent-bin"
      empty_dir {}
    }
    restart_policy = "Never"
  }
}
`,
	},
	{
		name:        "pod-with-pvc",
		description: "Pod with persistent storage (PVC survives stop/start) and terminal access",
		hcl: `terraform {
  required_providers {
    kubernetes = { source = "hashicorp/kubernetes" }
  }
}

variable "transition"    { default = "start" }
variable "name"          { default = "pod-with-pvc" }
variable "namespace"     { default = "default" }
variable "image"         { default = "ubuntu:22.04" }
variable "storage_size"  { default = "5Gi" }
variable "agent_token"   { default = "" }
variable "tahini_url"    { default = "" }

provider "kubernetes" {}

resource "kubernetes_persistent_volume_claim_v1" "data" {
  metadata {
    name      = "${var.name}-data"
    namespace = var.namespace
  }
  spec {
    access_modes = ["ReadWriteOnce"]
    resources {
      requests = {
        storage = var.storage_size
      }
    }
  }
  wait_until_bound = false
}

resource "kubernetes_pod_v1" "main" {
  count = var.transition == "stop" ? 0 : 1
  metadata {
    name      = var.name
    namespace = var.namespace
  }
  spec {
    init_container {
      name              = "tahini-agent-init"
      image             = "tahini-agent:latest"
      image_pull_policy = "IfNotPresent"
      command           = ["cp", "/tahini-agent", "/agent-bin/tahini-agent"]
      volume_mount {
        name       = "agent-bin"
        mount_path = "/agent-bin"
      }
    }
    container {
      name    = "main"
      image   = var.image
      command = ["sh", "-c", "/agent-bin/tahini-agent & sleep infinity"]
      env {
        name  = "TAHINI_URL"
        value = var.tahini_url
      }
      env {
        name  = "TAHINI_AGENT_TOKEN"
        value = var.agent_token
      }
      volume_mount {
        name       = "data"
        mount_path = "/data"
      }
      volume_mount {
        name       = "agent-bin"
        mount_path = "/agent-bin"
      }
    }
    volume {
      name = "data"
      persistent_volume_claim {
        claim_name = kubernetes_persistent_volume_claim_v1.data.metadata[0].name
      }
    }
    volume {
      name = "agent-bin"
      empty_dir {}
    }
    restart_policy = "Never"
  }
}
`,
	},
	{
		name:        "deployment",
		description: "Kubernetes Deployment with a Service",
		hcl: `terraform {
  required_providers {
    kubernetes = { source = "hashicorp/kubernetes" }
  }
}

variable "transition" { default = "start" }
variable "name"       { default = "my-deployment" }
variable "namespace"  { default = "default" }
variable "image"      { default = "nginx:alpine" }
variable "replicas"   { default = "2" }
variable "port"       { default = "80" }
variable "agent_token" { default = "" }
variable "tahini_url"  { default = "" }

provider "kubernetes" {}

locals {
  replicas = var.transition == "stop" ? 0 : tonumber(var.replicas)
}

resource "kubernetes_deployment_v1" "main" {
  metadata {
    name      = var.name
    namespace = var.namespace
  }
  spec {
    replicas = local.replicas
    selector {
      match_labels = {
        app = var.name
      }
    }
    template {
      metadata {
        labels = {
          app = var.name
        }
      }
      spec {
        container {
          name  = "main"
          image = var.image
          port {
            container_port = tonumber(var.port)
          }
        }
      }
    }
  }
}

resource "kubernetes_service_v1" "main" {
  metadata {
    name      = var.name
    namespace = var.namespace
  }
  spec {
    selector = {
      app = var.name
    }
    port {
      port        = tonumber(var.port)
      target_port = tonumber(var.port)
    }
  }
}
`,
	},
	{
		name:        "job",
		description: "Kubernetes batch Job",
		hcl: `terraform {
  required_providers {
    kubernetes = { source = "hashicorp/kubernetes" }
  }
}

variable "transition"  { default = "start" }
variable "name"        { default = "batch-job" }
variable "namespace"   { default = "default" }
variable "image"       { default = "python:3.12-slim" }
variable "command"     { default = "python -c \"print('hello from tahini job'); import time; time.sleep(10); print('done')\"" }
variable "agent_token" { default = "" }
variable "tahini_url"  { default = "" }

provider "kubernetes" {}

resource "kubernetes_job_v1" "main" {
  count = var.transition == "delete" ? 0 : 1
  metadata {
    name      = var.name
    namespace = var.namespace
  }
  spec {
    template {
      metadata {}
      spec {
        container {
          name    = "main"
          image   = var.image
          command = ["sh", "-c", var.command]
        }
        restart_policy = "Never"
      }
    }
    backoff_limit = 2
  }
  wait_for_completion = false
}
`,
	},
	{
		name:        "tensorflow-pod",
		description: "TensorFlow CPU pod with persistent workspace and terminal access",
		hcl: `terraform {
  required_providers {
    kubernetes = { source = "hashicorp/kubernetes" }
  }
}

variable "transition"      { default = "start" }
variable "name"            { default = "tensorflow-pod" }
variable "namespace"       { default = "default" }
variable "tf_version"      { default = "2.16.0" }
variable "storage_size"    { default = "10Gi" }
variable "cpu_request"     { default = "1" }
variable "memory_request"  { default = "2Gi" }
variable "agent_token"     { default = "" }
variable "tahini_url"      { default = "" }

provider "kubernetes" {}

resource "kubernetes_persistent_volume_claim_v1" "workspace" {
  metadata {
    name      = "${var.name}-workspace"
    namespace = var.namespace
  }
  spec {
    access_modes = ["ReadWriteOnce"]
    resources {
      requests = {
        storage = var.storage_size
      }
    }
  }
  wait_until_bound = false
}

resource "kubernetes_pod_v1" "main" {
  count = var.transition == "stop" ? 0 : 1
  metadata {
    name      = var.name
    namespace = var.namespace
  }
  spec {
    init_container {
      name              = "tahini-agent-init"
      image             = "tahini-agent:latest"
      image_pull_policy = "IfNotPresent"
      command           = ["cp", "/tahini-agent", "/agent-bin/tahini-agent"]
      volume_mount {
        name       = "agent-bin"
        mount_path = "/agent-bin"
      }
    }
    container {
      name    = "tensorflow"
      image   = "tensorflow/tensorflow:${var.tf_version}"
      command = ["sh", "-c", "/agent-bin/tahini-agent & sleep infinity"]
      resources {
        requests = {
          cpu    = var.cpu_request
          memory = var.memory_request
        }
      }
      env {
        name  = "TAHINI_URL"
        value = var.tahini_url
      }
      env {
        name  = "TAHINI_AGENT_TOKEN"
        value = var.agent_token
      }
      volume_mount {
        name       = "workspace"
        mount_path = "/workspace"
      }
      volume_mount {
        name       = "agent-bin"
        mount_path = "/agent-bin"
      }
    }
    volume {
      name = "workspace"
      persistent_volume_claim {
        claim_name = kubernetes_persistent_volume_claim_v1.workspace.metadata[0].name
      }
    }
    volume {
      name = "agent-bin"
      empty_dir {}
    }
    restart_policy = "Never"
  }
}
`,
	},
}
