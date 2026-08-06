---
title: MCP Tool Registry Reference
description: Complete tool definitions, scopes, risk levels, and descriptions exposed by Inroad MCP server.
---

The following tools are registered in `internal/app/agenttool` and exposed over `/v1/mcp` based on granted OAuth scopes.

## Contacts & CRM Tools

| Tool Name | Required Scope | Risk Level | Description |
| :--- | :--- | :--- | :--- |
| `inroad_contact_read` | `contacts:read` | `Read` | Search and retrieve contact records by ID or filter. |
| `inroad_contact_write` | `contacts:write` | `Write` | Create or update contact records. |
| `inroad_contacts_import` | `contacts:write` | `Write` | Bulk import contacts into a specific list. |
| `inroad_company_read` | `crm:read` | `Read` | List or view CRM companies. |
| `inroad_company_write` | `crm:write` | `Write` | Create or update a CRM company. |
| `inroad_deal_read` | `crm:read` | `Read` | List deals or view pipeline stages. |
| `inroad_deal_write` | `crm:write` | `Write` | Create, update, or transition deal pipeline stage. |
| `inroad_events_read` | `crm:read` | `Read` | View contact/deal activity feed. |
| `inroad_thread_read` | `crm:read` | `Read` | View CRM email thread metadata. |

## Campaign & Mailbox Tools

| Tool Name | Required Scope | Risk Level | Description |
| :--- | :--- | :--- | :--- |
| `inroad_campaign_read` | `campaigns:read` | `Read` | List campaigns and read sequence steps. |
| `inroad_campaign_control` | `campaigns:write` | `Consequential` | Pause, resume, or control campaign execution. |
| `inroad_mailbox_read` | `mailboxes:read` | `Read` | List connected mailboxes and health status. |
| `inroad_warmup_read` | `mailboxes:read` | `Read` | Read mailbox warmup placement and health metrics. |
| `inroad_deliverability_read` | `campaigns:read` | `Read` | Read deliverability circuit breaker telemetry. |
| `inroad_list_read` | `lists:read` | `Read` | List contact lists. |
| `inroad_list_write` | `lists:write` | `Write` | Create or edit contact lists. |
