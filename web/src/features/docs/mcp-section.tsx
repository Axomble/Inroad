import { Badge } from '@/components/ui/badge'
import { SectionBar, ListHeader, ListHeaderCell } from '@/components/layout/page'
import { MCP_TOOLS, OAUTH_GRANTABLE_SCOPES, type ToolRisk } from './docs-data'

const riskBadge: Record<ToolRisk, { variant: 'secondary' | 'default' | 'warm' | 'danger'; label: string }> = {
  read: { variant: 'secondary', label: 'read' },
  write: { variant: 'default', label: 'write' },
  consequential: { variant: 'warm', label: 'consequential' },
  irreversible: { variant: 'danger', label: 'irreversible' },
}

/**
 * How to connect an MCP client, transcribed from the real mount points:
 * /v1/mcp is a stateless Streamable HTTP endpoint accepting OAuth 2.1 bearer
 * tokens only (cmd/inroad/main.go wires oauthVerifier.VerifyToken; API keys
 * authenticate the REST surface, not MCP), with RFC 9728 discovery at
 * /.well-known/oauth-protected-resource.
 */
export function McpSection() {
  return (
    <section aria-labelledby="docs-mcp">
      <SectionBar label="MCP server" count={MCP_TOOLS.length} />
      <div className="border-b border-border px-4 py-5 sm:px-6">
        <h2 id="docs-mcp" className="sr-only">
          MCP server
        </h2>
        <p className="max-w-2xl text-sm text-muted-foreground">
          Inroad exposes its agent tools over the Model Context Protocol at{' '}
          <code className="font-mono text-[12px] text-foreground">/v1/mcp</code> — Streamable HTTP transport
          (not SSE), stateless, authenticated with OAuth 2.1 bearer tokens. Clients discover the authorization
          server via{' '}
          <code className="font-mono text-[12px] text-foreground">/.well-known/oauth-protected-resource</code>.
        </p>
        <ol className="mt-4 max-w-2xl list-decimal space-y-2 pl-5 text-sm text-muted-foreground">
          <li>
            A workspace admin registers an OAuth app under{' '}
            <span className="text-foreground">Settings → Connected apps</span> (or the client self-registers at{' '}
            <code className="font-mono text-[12px] text-foreground">/oauth2/register</code>).
          </li>
          <li>
            The client runs the OAuth 2.1 flow (
            <code className="font-mono text-[12px] text-foreground">/oauth2/authorize</code> →{' '}
            <code className="font-mono text-[12px] text-foreground">/oauth2/token</code>) and holds an access
            token scoped to what you granted.
          </li>
          <li>
            Point the MCP client at{' '}
            <code className="font-mono text-[12px] text-foreground">https://your-deployment/v1/mcp</code> with
            that bearer token. The tool list it sees is filtered to the granted scopes, and every call executes
            as the authorizing user with the same tenant and role checks as the UI.
          </li>
        </ol>
        <p className="mt-4 max-w-2xl text-sm text-muted-foreground">
          API keys (<code className="font-mono text-[12px] text-foreground">inrd_…</code>, Settings → API keys)
          authenticate the REST API under{' '}
          <code className="font-mono text-[12px] text-foreground">/api/v1</code>; the MCP endpoint accepts OAuth
          tokens only. Grantable scopes:{' '}
          {OAUTH_GRANTABLE_SCOPES.map((scope, i) => (
            <span key={scope}>
              {i > 0 && ', '}
              <code className="font-mono text-[11px] text-foreground">{scope}</code>
            </span>
          ))}
          . Sending mail, mutating campaigns, mutating mailboxes, and reading inbox bodies are structurally
          excluded from OAuth grants.
        </p>
      </div>

      <ListHeader>
        <ListHeaderCell className="w-56">Tool</ListHeaderCell>
        <ListHeaderCell className="w-32">Scope</ListHeaderCell>
        <ListHeaderCell className="w-32">Risk</ListHeaderCell>
        <ListHeaderCell className="min-w-0 flex-1">What it does</ListHeaderCell>
      </ListHeader>
      {MCP_TOOLS.map((tool) => {
        const risk = riskBadge[tool.risk]
        return (
          <div key={tool.name} role="row" className="flex items-center gap-4 border-b border-border px-5 py-2.5">
            <code className="w-56 shrink-0 truncate font-mono text-[12px] text-foreground">{tool.name}</code>
            <code className="w-32 shrink-0 font-mono text-[11px] text-muted-foreground">{tool.scope}</code>
            <span className="flex w-32 shrink-0 items-center gap-1">
              <Badge variant={risk.variant}>{risk.label}</Badge>
              {tool.adminOnly && <Badge variant="outline">admin</Badge>}
            </span>
            <span className="min-w-0 flex-1 truncate text-[12.5px] text-muted-foreground" title={tool.description}>
              {tool.description}
            </span>
          </div>
        )
      })}

      <div className="border-b border-border px-4 py-4 text-[12.5px] text-muted-foreground sm:px-6">
        Risk tiers gate execution, not visibility: <span className="text-foreground">read</span> tools always run,{' '}
        <span className="text-foreground">write</span> tools run but are attributed and revertible, and{' '}
        <span className="text-foreground">consequential</span> calls park in the approval queue for a human first.
        An <span className="text-foreground">irreversible</span> tier exists as the always-approval backstop; no
        current tool is in it.
      </div>
    </section>
  )
}
