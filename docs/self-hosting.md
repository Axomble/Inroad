# Self-Hosting Inroad

## Requirements
- Docker + Docker Compose

## Run
    cp .env.example .env
    # Generate real secrets (the compose stack refuses to start without them):
    #   INROAD_JWT_SECRET   = openssl rand -base64 32
    #   INROAD_MASTER_KEY   = openssl rand -base64 32   (must decode to 32 bytes)
    # Put both in .env, then:
    docker compose up --build

The API (with the built web UI) serves on http://localhost:8080. Migrations run
automatically on the api container's startup. The worker connects to Redis.

## Production notes
- Set strong INROAD_JWT_SECRET and INROAD_MASTER_KEY (see .env.example for generation).
- Put a TLS-terminating reverse proxy in front of :8080.
- For worker fleets across multiple IPs, run the worker binary under systemd
  (templates in deploy/systemd/) rather than compose.
