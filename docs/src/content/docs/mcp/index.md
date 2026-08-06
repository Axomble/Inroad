---
title: MCP Server Overview (/v1/mcp)
description: Introduction to Inroad Model Context Protocol (MCP) server, OAuth 2.1 authentication, and AI agent integration.
---

Inroad implements a native **Model Context Protocol (MCP)** server over Streamable HTTP at `/v1/mcp`.

## Architecture & Capabilities

The MCP server adapts `internal/app/agenttool.Registry` into an interactive MCP endpoint compliant with `@modelcontextprotocol/go-sdk` v1.7.0.

- **Endpoint:** `/v1/mcp`
- **Metadata Endpoint:** `/.well-known/oauth-protected-resource` (RFC 9728)
- **Authorization:** OAuth 2.1 Bearer Token (`Authorization: Bearer inoa_...`) or session-scoped tokens.
- **Dynamic Tool Discovery:** Exposes domain tools matching granted OAuth scopes (`contacts:read`, `contacts:write`, `crm:read`, `crm:write`, `campaigns:read`, `mailboxes:read`, `lists:read`, `lists:write`).

## Authentication Flow

1. Register an OAuth client at `/oauth2/register`.
2. Direct user to `/oauth2/authorize` with requested scopes.
3. Exchange authorization code at `/oauth2/token` for an access token (`inoa_...`).
4. Connect MCP Client (Claude Desktop, Cursor, Windsurf, LangChain) with `Authorization: Bearer inoa_...`.
