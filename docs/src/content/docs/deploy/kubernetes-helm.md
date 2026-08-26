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

## Before raising `replicas`

Two settings are per-process and become wrong the moment there is more than one
worker pod. Both are covered in detail in
[Environment Variables](/deploy/environment-variables/):

- **`INROAD_DB_MAX_CONNS`** — the pgx pool is per pod, but Postgres
  `max_connections` is shared. Keep `replicas × INROAD_DB_MAX_CONNS + headroom ≤
  max_connections`; at the default of `25`, four pods reach a stock `100`
  exactly, and the symptom is silent `pool.Acquire` blocking rather than an error.
- **`INROAD_RUN_SCHEDULER`** — asynq elects no leader, so every worker pod with
  this on registers the periodic sweeps and each fires once per pod. Set it
  `false` on every worker pod except one. The simplest shape is a second
  single-replica worker Deployment with the flag on, and the scaled Deployment
  with it off.
