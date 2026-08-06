---
title: Core Security Invariants
description: Mandatory security rules, multi-tenancy context derivation, envelope encryption (DEK/KEK), and SSRF protections.
---

Inroad observes strict security invariants across every package and change.

## 1. Context-Derived Multi-Tenancy
- Every tenant-scoped database query **MUST** filter by `workspace_id` from the verified JWT context (`auth.UserFromContext`), never trusted from client request parameters.
- Composite DB foreign keys (e.g. `campaign_senders(campaign_id, workspace_id)`) enforce isolation at the PostgreSQL schema level.

## 2. Two-Level Envelope Cryptography (DEK / KEK)
- Mailbox credentials and OAuth tokens are AES-256-GCM encrypted using per-workspace 32-byte Data Encryption Keys (DEKs).
- DEKs are wrapped using `crypto.KeyProvider` (`LocalKeyProvider` using HKDF-SHA256 of `INROAD_MASTER_KEY` or `aws-kms`).
- AES-GCM Additional Authenticated Data (`ws:<uuid>`) binds ciphertexts to the workspace ID.
- Workspace deletion cascades `workspace_deks`, providing instant crypto-shredding for GDPR compliance.

## 3. Outbound SSRF Protection (`mail.vetAddr`)
- User-supplied mail server hosts dial through `mail.vetAddr` (blocks loopback, link-local `169.254.169.254`, multicast, and private RFC1918 ranges unless `INROAD_MAIL_ALLOW_PRIVATE_HOSTS=true`).
- Dials the resolved IP directly while retaining host string for TLS ServerName verification to eliminate DNS rebinding risks.

## 4. Claim-Before-Send Idempotency
- Worker send steps execute an atomic DB claim (`queued` → `sending` with lease timestamp) BEFORE network SMTP calls to eliminate duplicate delivery on retries.
