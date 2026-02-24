# X-Ray Agentd — Cloud Run deployment via Terraform
#
# Usage:
#   cd deploy
#   terraform init
#   terraform apply -var="project_id=your-project"
#
# Or with deploy.sh for a quick gcloud-only deploy.

variable "project_id" {
  description = "GCP project ID"
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
  description = "Container image URL (built via deploy.sh or Cloud Build)"
  type        = string
  default     = ""
}

locals {
  service_name = "x-ray-agentd"
  image        = var.image != "" ? var.image : "gcr.io/${var.project_id}/${local.service_name}"
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
  service = "run.googleapis.com"
}

resource "google_project_service" "build" {
  service = "cloudbuild.googleapis.com"
}

resource "google_project_service" "secretmanager" {
  service = "secretmanager.googleapis.com"
}

# Cloud Run service
resource "google_cloud_run_v2_service" "agentd" {
  name     = local.service_name
  location = var.region

  depends_on = [google_project_service.run]

  template {
    containers {
      image = local.image

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

      # TCP probe — agentd has no HTTP health endpoint, just check the port is listening.
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
}

# Allow unauthenticated access (extension connects directly)
resource "google_cloud_run_v2_service_iam_member" "public" {
  name     = google_cloud_run_v2_service.agentd.name
  location = var.region
  role     = "roles/run.invoker"
  member   = "allUsers"
}

output "service_url" {
  value = google_cloud_run_v2_service.agentd.uri
}
