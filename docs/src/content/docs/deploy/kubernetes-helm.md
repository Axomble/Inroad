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

:::caution[Scaling `deployment-worker.yaml` does not diversify sending IPs]
A cluster's CNI typically NATs every pod through a small, fixed set of node or
cluster egress IPs, and pods get rescheduled onto different nodes across
deploys and autoscaling events. Raising `replicas` here buys throughput on
whatever IP identity already exists — it does not give Inroad's fleet
placement (provider crowding, blast-radius scoring, per-worker egress IP
affinity) anything new to spread mailboxes across, and a pod that gets
rescheduled mid-warmup changes the client address a mailbox's provider sees
for no reason, which costs sign-in trust rather than building it.

This chart is a solid fit for the **control plane** — API pods, and a
`INROAD_WORKER_ROLE=control` worker pod for the scheduler and sweeps. For the
sending fleet, prefer individually-addressed hosts outside the cluster: run
`INROAD_WORKER_ROLE=send` on a plain VPS per desired sending identity, with
`INROAD_WORKER_EGRESS_IP` set to that host's own address, pointed at this
cluster's Postgres/Redis. See
[Splitting control and send roles](/deploy/environment-variables/#splitting-control-and-send-roles)
and the mailbox guide's [per-worker egress IP routing](/guides/mailboxes/).
:::
