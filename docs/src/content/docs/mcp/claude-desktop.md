---
title: Claude Desktop Integration
description: Step-by-step setup guide for connecting Claude Desktop to Inroad MCP server.
---

Connect **Claude Desktop** to Inroad's MCP server to let Claude inspect contacts, manage CRM records, and view campaign stats directly from conversation.

## Configuration File

Add the following to your `claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "inroad": {
      "command": "npx",
      "args": [
        "-y",
        "@modelcontextprotocol/server-http-client",
        "https://inroad.example.com/v1/mcp",
        "--header",
        "Authorization: Bearer YOUR_OAUTH_ACCESS_TOKEN"
      ]
    }
  }
}
```

Replace `YOUR_OAUTH_ACCESS_TOKEN` with your active OAuth access token or API token.
