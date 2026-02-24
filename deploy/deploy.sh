#!/bin/bash
# Builds and deploys agentd to Cloud Run.
# Works from Apple Silicon — Cloud Build runs linux/amd64 natively.
#
# Prerequisites:
#   export GCP_PROJECT_ID="your-project"
#   gcloud auth login
#   # Create the secret once:
#   echo -n "your-gemini-key" | gcloud secrets create gemini-api-key --data-file=-

set -euo pipefail

PROJECT_ID="${GCP_PROJECT_ID:?Set GCP_PROJECT_ID}"
REGION="${GCP_REGION:-us-central1}"
SERVICE_NAME="x-ray-agentd"
SECRET_NAME="${GEMINI_SECRET_NAME:-gemini-api-key}"

echo "Building and pushing via Cloud Build..."
gcloud builds submit --tag "gcr.io/$PROJECT_ID/$SERVICE_NAME" \
  --project "$PROJECT_ID"

echo "Deploying to Cloud Run..."
gcloud run deploy "$SERVICE_NAME" \
  --image "gcr.io/$PROJECT_ID/$SERVICE_NAME" \
  --platform managed \
  --region "$REGION" \
  --allow-unauthenticated \
  --set-secrets "GOOGLE_API_KEY=$SECRET_NAME:latest" \
  --port 8080 \
  --project "$PROJECT_ID"

echo "Deployed to: $(gcloud run services describe "$SERVICE_NAME" --region "$REGION" --format 'value(status.url)' --project "$PROJECT_ID")"
