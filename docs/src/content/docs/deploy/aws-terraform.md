---
title: AWS Production Deployment (Terraform)
description: Production-grade enterprise deployment on AWS using Terraform (RDS, ECS Fargate, S3, ElastiCache, KMS).
---

For high-volume production deployments, Inroad provides complete Infrastructure as Code (IaC) Terraform modules in `deploy/terraform/aws/`.

## Architecture Infrastructure

- **VPC Subnets:** Public, Private App, and Private Database subnets across 2 Availability Zones with NAT Gateways.
- **Managed Database:** Amazon RDS PostgreSQL 16 (Multi-AZ encrypted).
- **In-Memory Cache:** Amazon ElastiCache for Redis cluster.
- **Container Compute:** AWS ECS Fargate Task Definitions & Services for API (`cmd/inroad`) and Worker (`cmd/worker`).
- **Load Balancing:** AWS Application Load Balancer (ALB) with HTTPS listener and `/healthz` health checks.
- **Encryption & Key Management:** AWS KMS Key for DEK envelope encryption (`aws-kms` provider).
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
