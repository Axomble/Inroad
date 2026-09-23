---
title: AWS Production Deployment (Terraform)
description: Production-grade enterprise deployment on AWS using Terraform (RDS, ECS Fargate, S3, ElastiCache, KMS).
---

For high-volume production deployments, Inroad provides complete Infrastructure as Code (IaC) Terraform modules in `deploy/terraform/aws/`.

:::caution[This module is a single-egress topology]
Every ECS Fargate task — API and worker alike — routes outbound traffic through
**one** NAT Gateway and **one** Elastic IP. Raising `desired_count` on the
worker service adds send throughput, not sending identities: every task still
shares that one IP, so it gives Inroad's fleet placement (provider crowding,
blast-radius scoring, [per-worker egress IP routing](/guides/mailboxes/))
nothing to actually spread work across.

If you want more than one sending identity, this module isn't where that comes
from. Two ways to get it:

- **One NAT Gateway + route table per IP**, extending this module's own
  pattern — this is the *expensive* way to do it on AWS specifically (flat
  per-gateway hourly billing regardless of instance count). See
  [Hosting Costs](/deploy/hosting-costs/) for real numbers and cheaper
  alternatives, including on other clouds.
- **Run `INROAD_WORKER_ROLE=send` workers on separate hosts outside this
  module** instead — a plain VPS from any provider gives you a distinct,
  stable IP for a fraction of the NAT Gateway cost, and is what the fleet's
  placement scoring was actually designed against. Point each host at
  `INROAD_DATABASE_URL` / `INROAD_REDIS_ADDR` from this stack, set
  `INROAD_WORKER_EGRESS_IP` to the host's own address, and keep
  `INROAD_WORKER_ROLE=control` on infrastructure you trust (see
  [Splitting control and send roles](/deploy/environment-variables/#splitting-control-and-send-roles)).

This module is a solid choice for the **control plane** — Postgres, Redis, the
API — where you want Multi-AZ durability and don't need IP diversity at all.
It is not, by itself, a multi-IP sending fleet.
:::

## Architecture Infrastructure

- **VPC Subnets:** Public, Private App, and Private Database subnets across 2 Availability Zones with NAT Gateways.
- **Managed Database:** Amazon RDS PostgreSQL 16 (Multi-AZ encrypted). Keep `engine_version` at 15 or above: PostgreSQL **15 or newer is required**; 16 is what every bundled deployment runs and what CI tests against. The schema uses column-list `ON DELETE SET NULL (column)` foreign keys (the conditional-branching migration, `20260923110214_sequence_step_branches`), which PostgreSQL 14 and older reject, so migrations stop there on an older server.
- **In-Memory Cache:** Amazon ElastiCache for Redis cluster.
- **Container Compute:** AWS ECS Fargate Task Definitions & Services for API (`cmd/inroad`) and Worker (`cmd/worker`).
- **Load Balancing:** AWS Application Load Balancer (ALB) with HTTPS listener and `/healthz` health checks.
- **Encryption & Key Management:** An AWS KMS key encrypts RDS, ElastiCache and S3
  **at rest**. It does *not* wrap Inroad's per-workspace DEKs: `INROAD_KEY_PROVIDER`
  accepts only `local` today and refuses to start on any other value, so the
  application's envelope encryption still derives its KEK from `INROAD_MASTER_KEY`.
  A `KMSKeyProvider` exists behind the same seam but is not yet selectable.
- **Object Storage:** Amazon S3 bucket for assets, mail attachments, and exports.

## Terraform Deployment

```bash
cd deploy/terraform/aws

# Initialize Terraform
terraform init

# Review execution plan
terraform plan

# Apply infrastructure
terraform apply
```
