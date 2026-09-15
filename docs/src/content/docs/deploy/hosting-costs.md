---
title: Hosting Costs
description: Official, cited pricing for getting distinct sending IPs onto Inroad's execution plane — AWS, GCP, and Azure against plain VPS providers.
---

The [AWS Terraform](/deploy/aws-terraform/) and [Kubernetes Helm](/deploy/kubernetes-helm/)
guides both warn that their default topology shares one egress IP across every
worker. This page prices out what it actually costs to fix that — across more
than one cloud, since the right answer is not the same on every one of them.

:::caution[Prices change; this table won't]
Every figure below was compiled from the official pricing pages linked in
each row, **in September 2026**. Cloud network pricing moves — AWS changed
Elastic IP billing in February 2024, and Azure retired Basic-SKU public IPs
in September 2025. Treat this page as a shape, not a quote: click through to
the source before you budget a real deployment.
:::

## The two shapes

A dedicated sending identity for an `INROAD_WORKER_ROLE=send` host
([per-worker egress IP routing](/guides/mailboxes/)) comes from one of two
patterns:

1. **A NAT gateway (or equivalent) per IP**, on a managed cloud, with the
   worker in a private subnet behind it.
2. **One small VPS per IP**, with the worker bound directly to the machine's
   own public address (`INROAD_WORKER_EGRESS_IP`) — no NAT involved at all.

## Pattern 1: managed-cloud NAT/IP, priced per provider

This is where "don't rely on one cloud" actually matters — the three major
clouds price this completely differently.

| Provider | Gateway cost | Public IP cost | **Cost per additional dedicated IP** | Source |
|---|---|---|---|---|
| **AWS** | NAT Gateway: $0.045/hr flat (≈$32.85/mo), **regardless of instance count behind it** | Elastic IP: $0.005/hr (≈$3.60/mo), billed whether attached or not | **≈$36.45/mo** + $0.045/GB processed | [aws.amazon.com/vpc/pricing](https://aws.amazon.com/vpc/pricing/) |
| **Azure** | NAT Gateway: $0.045/hr flat (≈$32.40/mo), same flat-fee model as AWS | Standard Public IP: $0.005/hr (≈$3.65/mo) | **≈$36.05/mo** + ≈$0.045/GB processed | [Azure NAT Gateway pricing](https://azure.microsoft.com/pricing/details/virtual-network/) |
| **GCP** | Cloud NAT: **$0.0014 per VM-hour actually behind the gateway**, capped at $0.044/hr once 32+ VMs share it | External static IP: $0.005/hr (≈$3.65/mo) | **≈$4.67/mo** for a gateway serving one VM (≈$1.02 + $3.65) + $0.045/GiB processed | [cloud.google.com/nat/pricing](https://cloud.google.com/nat/pricing) |

The load-bearing difference: **AWS and Azure charge per gateway, GCP charges
per instance behind the gateway.** Put one worker behind its own NAT gateway
for an isolated IP, and AWS/Azure charge you the same flat ≈$32/mo whether
that gateway serves 1 VM or 50. GCP charges for the VM you actually put
there — about $1/mo at one VM. That makes "one NAT gateway per sending
identity" roughly **8x cheaper on GCP than on AWS or Azure**, for
functionally the same isolation.

Base compute for the worker itself, on the cheapest ARM-ish instance each
cloud offers (needed either way, so not part of the "per IP" delta above):

| Provider | Instance | Price | Source |
|---|---|---|---|
| AWS | t4g.nano | $0.0042/hr (≈$3.07/mo) | [aws.amazon.com/ec2/pricing/on-demand](https://aws.amazon.com/ec2/pricing/on-demand/) |
| AWS | t4g.micro | $0.0084/hr (≈$6.13/mo) | same |
| GCP | e2-micro | ≈$0.0084/hr (≈$6.11/mo); free-tier eligible in some US regions | [cloud.google.com/compute/all-pricing](https://cloud.google.com/compute/all-pricing) |

Control-plane durable services — the part that genuinely benefits from a
managed cloud — price similarly across providers for burstable/smallest
tiers: AWS RDS `db.t4g.micro` and ElastiCache `cache.t4g.micro` are both
$0.016/hr (≈$11.68/mo) in us-east-1
([RDS pricing](https://aws.amazon.com/rds/pricing/),
[ElastiCache pricing](https://aws.amazon.com/elasticache/pricing/)); GCP
Cloud SQL and Memorystore, and Azure Database for PostgreSQL and Azure Cache
for Redis, land in the same range at their smallest burstable tiers. Only
`deploy/terraform/aws/` ships as code in this repo today — the other two
clouds are priced here as options, not as something Inroad automates yet.

## Pattern 2: one VPS per IP

No NAT, no gateway fee — the machine's own address is the identity.

| Provider | Cheapest instance | Additional/extra IPv4 | **Effective cost per IP-bearing host** | Source |
|---|---|---|---|---|
| **Hetzner** | CX22 (2 vCPU/4GB): €3.79/mo | €0.50/mo per additional IPv4 | **≈€4.29/mo** (≈$4.60–$4.70, FX-dependent) | [hetzner.com/cloud](https://www.hetzner.com/cloud/) |
| **DigitalOcean** | Basic Droplet: $4/mo | A Droplet's own public IP is free and stable for its lifetime; a *movable* Reserved IP is $5/mo only while unattached | **≈$4/mo** | [DigitalOcean Reserved IP pricing](https://docs.digitalocean.com/products/networking/reserved-ips/details/pricing/) |
| **Vultr** | Cloud Compute w/ IPv4: $3.50/mo | Additional IPv4 add-on pricing isn't published on the general pricing page — check the console for your region | **≈$3.50/mo** base | [vultr.com/pricing](https://www.vultr.com/pricing/) |

Each of these gives you a genuinely distinct, stable public IP with no shared
NAT in the path at all — at roughly **an eighth to a tenth of the AWS/Azure
NAT-per-IP cost**, and still cheaper than GCP's per-VM Cloud NAT rate once
you count in the instance itself.

## Worked totals

Assuming the AWS/Azure control-plane baseline from
[the Terraform doc](/deploy/aws-terraform/) (≈$160–180/mo with Multi-AZ) stays
fixed, and only the sending fleet's IP count changes:

| Sending IPs | AWS NAT-per-IP | AWS EC2+EIP-per-IP | GCP Cloud-NAT-per-IP | VPS-per-IP (Hetzner/DO) |
|---|---|---|---|---|
| 1 (baseline) | included | included | included | included |
| 3 | +2 × $36.45 ≈ **+$73** | +2 × ≈$9.70 ≈ **+$19** | +2 × ≈$4.67 ≈ **+$9** | +2 × ≈$4 ≈ **+$8** |
| 5 | +4 × $36.45 ≈ **+$146** | +4 × ≈$9.70 ≈ **+$39** | +4 × ≈$4.67 ≈ **+$19** | +4 × ≈$4 ≈ **+$16** |
| 10 | +9 × $36.45 ≈ **+$328** | +9 × ≈$9.70 ≈ **+$87** | +9 × ≈$4.67 ≈ **+$42** | +9 × ≈$4 ≈ **+$36** |

## Recommendation

Keep the durable, stateful pieces — Postgres, Redis, KMS — on whichever
managed cloud you already trust for backups and durability; none of that
needs IP diversity, and the AWS Terraform module or an equivalent GCP/Azure
setup are both fine there. For the `send`-role fleet itself, the NAT-gateway
pattern is the expensive way to get there on AWS or Azure specifically — if
you want to stay on one of those two clouds, EC2/Compute-instance-plus-EIP
per IP is the cheaper in-cloud option; GCP's per-VM Cloud NAT pricing closes
most of that gap without leaving GCP at all; and a handful of plain VPS
hosts, one per sending identity, remains the cheapest and simplest path
regardless of which cloud runs the control plane.
