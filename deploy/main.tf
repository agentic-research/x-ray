# X-Ray Agentd — Cloud Run deployment via Terraform + ko
#
# Usage:
#   export TF_VAR_project_id=your-gcp-project-id
#   export KO_DOCKER_REPO=us-central1-docker.pkg.dev/$TF_VAR_project_id/x-ray
#   ko build ./cmd/agentd          # builds & pushes image
#   cd deploy && terraform init && terraform apply
#
# The image var is set by the deploy task (ko build --bare outputs the digest).

variable "project_id" {
  description = "GCP project ID (set via TF_VAR_project_id env var)"
  type        = string
}

variable "region" {
  description = "GCP region for Cloud Run"
  type        = string
  default     = "us-central1"
}

variable "gemini_api_key_secret" {
  description = "Secret Manager secret name for Gemini API key"
  type        = string
  default     = "gemini-api-key"
}

variable "image" {
  description = "Container image URL (output of ko build)"
  type        = string
}

locals {
  service_name = "x-ray-agentd"
}

terraform {
  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 5.0"
    }
  }
}

provider "google" {
  project = var.project_id
  region  = var.region
}

# Enable required APIs
resource "google_project_service" "run" {
  service            = "run.googleapis.com"
  disable_on_destroy = false
}

resource "google_project_service" "artifactregistry" {
  service            = "artifactregistry.googleapis.com"
  disable_on_destroy = false
}

resource "google_project_service" "secretmanager" {
  service            = "secretmanager.googleapis.com"
  disable_on_destroy = false
}

# Artifact Registry repo for ko-built images
resource "google_artifact_registry_repository" "x_ray" {
  repository_id = "x-ray"
  location      = var.region
  format        = "DOCKER"

  depends_on = [google_project_service.artifactregistry]
}

# Cloud Run service
resource "google_cloud_run_v2_service" "agentd" {
  name     = local.service_name
  location = var.region

  depends_on = [google_project_service.run]

  template {
    containers {
      image = var.image

      ports {
        container_port = 8080
      }

      env {
        name = "GOOGLE_API_KEY"
        value_source {
          secret_key_ref {
            secret  = var.gemini_api_key_secret
            version = "latest"
          }
        }
      }

      resources {
        limits = {
          cpu    = "1"
          memory = "512Mi"
        }
      }

      startup_probe {
        tcp_socket {
          port = 8080
        }
        initial_delay_seconds = 0
        period_seconds        = 3
        failure_threshold     = 3
      }
    }

    # Voice sessions can be long — 30 min max.
    timeout = "1800s"

    scaling {
      min_instance_count = 0
      max_instance_count = 1
    }
  }

  # Only allow traffic from internal sources and Cloud Load Balancing — not the public internet.
  ingress = "INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER"
}

# No public access — use `gcloud run services proxy` for authenticated local access.
# The deploying user's account already has roles/run.invoker via project-level IAM.

output "service_url" {
  value = google_cloud_run_v2_service.agentd.uri
}
