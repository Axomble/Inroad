---
title: Kubernetes Cluster Deployment (Helm)
description: Production Helm chart deployment guide for EKS, AKS, GKE, or self-hosted Kubernetes clusters.
---

Inroad provides a production Helm v3 chart located in `deploy/helm/inroad/`.

## Helm Installation

```bash
# Add namespace
kubectl create namespace inroad

# Deploy Helm release
helm upgrade --install inroad ./deploy/helm/inroad \
  --namespace inroad \
  --set config.publicUrl="https://inroad.example.com" \
  --set secrets.jwtSecret="$(openssl rand -base64 32)" \
  --set secrets.masterKey="$(openssl rand -base64 32)"
```

## Chart Components

- **`deployment-api.yaml`:** API server pods with HTTP liveness/readiness probes.
- **`deployment-worker.yaml`:** Background worker pods.
- **`ingress.yaml`:** Ingress controller configuration with cert-manager TLS annotations.
- **`configmap.yaml` & `secret.yaml`:** Environment configuration and sealed credentials.
