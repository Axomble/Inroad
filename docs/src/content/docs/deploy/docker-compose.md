---
title: Single-Instance Docker Compose Self-Hosting
description: Zero-configuration single-command self-hosting guide for Inroad using Docker Compose.
---

Inroad provides a canonical, single-command Docker Compose setup suitable for self-hosting on any single VPS, EC2 instance, or dedicated server.

## Quick Start (Zero-Config)

Clone the repository and run:

```bash
git clone https://github.com/inroad/inroad.git
cd inroad

# Boots PostgreSQL, Redis, Auto-Migrations, API, Worker, and Web SPA
docker compose up -d
```

Open `http://localhost` (or your server's IP) in your browser.

## Service Composition

The unified `docker-compose.yml` includes 6 services:

- `postgres`: PostgreSQL 16 database.
- `redis`: Redis 7 in-memory queue & cache.
- `migrate`: Automatic schema migration service (`go run ./cmd/migrate up`).
- `api`: Control plane REST API server (`cmd/inroad`).
- `worker`: Execution plane background worker (`cmd/worker`).
- `web`: Optimized Nginx container hosting the React SPA on Port 80 and reverse proxying `/api/` calls to the API server.

## Live-Reloading Development Override

For local development with Go `air` hot-reloading and Vite HMR:

```bash
cp docker-compose.override.yml.example docker-compose.override.yml
docker compose up -d
```
