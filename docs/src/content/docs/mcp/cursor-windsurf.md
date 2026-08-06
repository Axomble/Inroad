---
title: Cursor & Windsurf Setup (.mcp.json)
description: Configure Cursor or Windsurf IDEs with Inroad MCP server.
---

Integrate Inroad MCP tools into **Cursor** or **Windsurf** for AI-assisted CRM and cold email workflows.

## Configuration (.mcp.json)

Create or update `.mcp.json` in your workspace root:

```json
{
  "mcpServers": {
    "inroad": {
      "url": "https://inroad.example.com/v1/mcp",
      "headers": {
        "Authorization": "Bearer YOUR_OAUTH_ACCESS_TOKEN"
      }
    }
  }
}
```
