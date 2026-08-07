---
title: MCP Tool Registry Reference
description: Complete tool definitions, scopes, risk levels, and descriptions exposed by Inroad MCP server.
---

The following tools are registered in `internal/app/agenttool` and exposed over `/v1/mcp` based on granted OAuth scopes (the scope column is `internal/app/mcpserver`'s `requiredScope` mapping).

## Search

| Tool Name | Required Scope | Risk Level | Description |
| :--- | :--- | :--- | :--- |
| `inroad_search` | `contacts:read` | `Read` | Cross-record search by name or email: campaigns, contacts, mailboxes, and contact lists in one call. |

## Contacts & CRM Tools

| Tool Name | Required Scope | Risk Level | Description |
| :--- | :--- | :--- | :--- |
| `inroad_contact_read` | `contacts:read` | `Read` | Search and retrieve contact records by ID or filter. |
| `inroad_contact_write` | `contacts:write` | `Write` | Create a contact, or add an existing contact to a list. |
| `inroad_contacts_import` | `contacts:write` | `Consequential` | Bulk import (>50 rows) into a list; every call parks in the approval queue for human review before executing. |
| `inroad_company_read` | `crm:read` | `Read` | List or view CRM companies. |
| `inroad_company_write` | `crm:write` | `Write` | Create or update a CRM company. |
| `inroad_pipeline_read` | `crm:read` | `Read` | Read CRM pipelines and their ordered stages. |
| `inroad_deal_read` | `crm:read` | `Read` | List deals, get one deal, or read a pipeline board with stage totals. |
| `inroad_deal_write` | `crm:write` | `Write` | Create, update, or transition deal pipeline stage. |
| `inroad_note_write` | `crm:write` | `Write` | Create a note attached to a contact, company, or deal. |
| `inroad_task_write` | `crm:write` | `Write` | Create a follow-up task attached to a contact, company, or deal. |
| `inroad_events_read` | `crm:read` | `Read` | View contact/company/deal activity feed. |
| `inroad_thread_read` | `crm:read` | `Read` | View CRM email thread metadata (participants and message metadata, never raw bodies). |

## Campaign & Mailbox Tools

| Tool Name | Required Scope | Risk Level | Description |
| :--- | :--- | :--- | :--- |
| `inroad_campaign_read` | `campaigns:read` | `Read` | List campaigns and read sequence configuration, stats, and enrollments. |
| `inroad_campaign_control` | `campaigns:write` | `Consequential` | Pause or resume a running campaign. Also requires the workspace `admin` role. Not reachable over MCP in practice: `campaigns:write` is excluded from `auth.OAuthGrantableScopes`. |
| `inroad_mailbox_read` | `mailboxes:read` | `Read` | List connected mailboxes and health status. |
| `inroad_warmup_read` | `mailboxes:read` | `Read` | Read mailbox warmup placement and health metrics. |
| `inroad_deliverability_read` | `campaigns:read` | `Read` | Read deliverability circuit breaker telemetry. |
| `inroad_list_read` | `lists:read` | `Read` | List contact lists. |
| `inroad_list_write` | `lists:write` | `Write` | Create contact lists. |

## Risk tiers

Tiers are a property of the tool, never the caller (`internal/app/agenttool/agenttool.go`): `Read` never mutates and always runs; `Write` is a reversible, attributed mutation; `Consequential` parks in the approval queue by default; `Irreversible` (sending mail, destroying data) always requires per-action human approval — no registered tool is currently in that tier.
