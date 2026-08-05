---
title: LangChain & Python/TS SDKs
description: Programmatic integration guide for Python and TypeScript AI agent frameworks.
---

Use Python or TypeScript to connect custom LLM agents (LangChain, LlamaIndex, AutoGen) to Inroad MCP tools.

## Python Integration

```python
from mcp.client.streamable_http import StreamableHTTPClient

async with StreamableHTTPClient(
    "https://inroad.example.com/v1/mcp",
    headers={"Authorization": "Bearer YOUR_OAUTH_ACCESS_TOKEN"}
) as client:
    tools = await client.list_tools()
    print("Available tools:", [t.name for t in tools])
```

## TypeScript Integration

```typescript
import { StreamableHTTPClient } from "@modelcontextprotocol/sdk/client/streamableHttp.js";

const client = new StreamableHTTPClient({
  url: "https://inroad.example.com/v1/mcp",
  headers: { Authorization: "Bearer YOUR_OAUTH_ACCESS_TOKEN" }
});

const tools = await client.listTools();
console.log("Tools:", tools);
```
