import { useState } from 'react'
import {
  Check,
  Copy,
  Cpu,
  Server,
  Zap,
  ShieldCheck,
  Search,
  Code2,
  Mail,
  Flame,
  Gauge,
  Users,
  MessageSquare,
  Terminal,
  Container,
  Cloud,
  ChevronRight,
  Key,
  Layers,
} from 'lucide-react'
import { Page, PageTopbar, PageBody } from '@/components/layout/page'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'

type DocCategory = 'features' | 'deploy' | 'mcp'

type DocSection =
  | 'overview'
  | 'campaigns'
  | 'warmup'
  | 'deliverability'
  | 'crm'
  | 'replyclassify'
  | 'docker'
  | 'terraform'
  | 'helm'
  | 'envvars'
  | 'mcp-overview'
  | 'mcp-oauth'
  | 'mcp-inspector'
  | 'mcp-snippets'

interface NavSection {
  id: DocSection
  label: string
  category: DocCategory
  icon: React.ComponentType<{ className?: string }>
}

const NAV_SECTIONS: { category: DocCategory; title: string; items: NavSection[] }[] = [
  {
    category: 'features',
    title: 'Platform Overview & Features',
    items: [
      { id: 'overview', label: 'Platform Architecture', category: 'features', icon: Layers },
      { id: 'campaigns', label: 'Campaign Sequences', category: 'features', icon: Mail },
      { id: 'warmup', label: 'Mailbox Warmup Engine', category: 'features', icon: Flame },
      { id: 'deliverability', label: 'Deliverability & DNS', category: 'features', icon: Gauge },
      { id: 'crm', label: 'CRM & Deal Pipeline', category: 'features', icon: Users },
      { id: 'replyclassify', label: 'Reply Classifier & AI', category: 'features', icon: MessageSquare },
    ],
  },
  {
    category: 'deploy',
    title: 'Deployment & Self-Hosting',
    items: [
      { id: 'docker', label: 'Docker Compose', category: 'deploy', icon: Container },
      { id: 'terraform', label: 'AWS ECS + RDS + S3', category: 'deploy', icon: Cloud },
      { id: 'helm', label: 'Kubernetes & Helm', category: 'deploy', icon: Server },
      { id: 'envvars', label: 'Environment Variables', category: 'deploy', icon: Key },
    ],
  },
  {
    category: 'mcp',
    title: 'MCP Server & Agent Hub',
    items: [
      { id: 'mcp-overview', label: 'MCP Protocol & /v1/mcp', category: 'mcp', icon: Cpu },
      { id: 'mcp-oauth', label: 'OAuth 2.1 Security', category: 'mcp', icon: ShieldCheck },
      { id: 'mcp-inspector', label: 'Tool Schema Inspector', category: 'mcp', icon: Code2 },
      { id: 'mcp-snippets', label: 'Snippet Generator', category: 'mcp', icon: Terminal },
    ],
  },
]

interface ToolSchema {
  name: string
  description: string
  parameters: Record<string, { type: string; description: string; required?: boolean }>
  exampleOutput: Record<string, unknown>
}

const MCP_TOOLS: ToolSchema[] = [
  {
    name: 'inroad_list_campaigns',
    description: 'Retrieve outreach campaigns for the current workspace with health metrics, status, and active senders.',
    parameters: {
      status: { type: 'string', description: 'Filter by status: draft, active, paused, archived', required: false },
      limit: { type: 'number', description: 'Number of results to return (default 20, max 100)', required: false },
    },
    exampleOutput: {
      campaigns: [
        {
          id: 'cmp_91f82a',
          name: 'Q3 Enterprise Founders Outreach',
          status: 'active',
          sent_count: 1420,
          reply_rate: 0.184,
          bounce_rate: 0.008,
          created_at: '2026-07-15T09:00:00Z',
        },
      ],
      total: 1,
    },
  },
  {
    name: 'inroad_create_campaign',
    description: 'Create a new multi-step cold outreach sequence with custom schedules, template variables, and fallback rules.',
    parameters: {
      name: { type: 'string', description: 'Campaign title', required: true },
      schedule: { type: 'string', description: 'Timezone-aware sending schedule ID or custom cron', required: true },
      steps: { type: 'array', description: 'List of sequence steps (email template, wait delay)', required: true },
    },
    exampleOutput: {
      campaign_id: 'cmp_02a7b8',
      name: 'Q3 Enterprise Founders Outreach',
      status: 'draft',
      steps_count: 3,
    },
  },
  {
    name: 'inroad_search_contacts',
    description: 'Query lead contacts by email, domain, company name, custom tags, or CRM deal stage.',
    parameters: {
      query: { type: 'string', description: 'Search term for name, email, or company', required: true },
      tag: { type: 'string', description: 'Filter by exact contact tag', required: false },
      limit: { type: 'number', description: 'Maximum contacts to fetch (default 50)', required: false },
    },
    exampleOutput: {
      contacts: [
        {
          id: 'cnt_88b12c',
          email: 'alex@acmecorp.io',
          first_name: 'Alex',
          company_name: 'Acme Corp',
          deal_stage: 'qualified',
          tags: ['icp-tier1', 'tech-founder'],
        },
      ],
    },
  },
  {
    name: 'inroad_enroll_contact',
    description: 'Enroll a contact into an active campaign sequence with optional initial field overrides.',
    parameters: {
      campaign_id: { type: 'string', description: 'Target active campaign UUID', required: true },
      contact_id: { type: 'string', description: 'Target contact UUID', required: true },
      custom_fields: { type: 'object', description: 'Template replacement key-value pairs', required: false },
    },
    exampleOutput: {
      enrollment_id: 'enr_44d91e',
      status: 'enrolled',
      scheduled_first_send: '2026-08-06T14:30:00Z',
    },
  },
  {
    name: 'inroad_get_deliverability_score',
    description: 'Check mailbox deliverability scores, SPF/DKIM/DMARC status, and spam trap warning metrics.',
    parameters: {
      mailbox_id: { type: 'string', description: 'Mailbox UUID to analyze', required: false },
    },
    exampleOutput: {
      overall_score: 98,
      spf_valid: true,
      dkim_valid: true,
      dmarc_valid: true,
      mx_valid: true,
      spam_trap_alerts: 0,
      inbox_placement_rate: 0.976,
    },
  },
  {
    name: 'inroad_pause_warmup',
    description: 'Pause or adjust automated peer-to-peer warmup schedules for specific mailboxes.',
    parameters: {
      mailbox_id: { type: 'string', description: 'Mailbox UUID to pause', required: true },
      reason: { type: 'string', description: 'Optional maintenance note', required: false },
    },
    exampleOutput: {
      mailbox_id: 'mbx_11e39a',
      warmup_status: 'paused',
      updated_at: '2026-08-06T01:52:00Z',
    },
  },
]

const ENV_VARS = [
  { name: 'INROAD_DATABASE_URL', category: 'Database', req: true, desc: 'PostgreSQL 16 connection string (pgx dialect).' },
  { name: 'INROAD_REDIS_URL', category: 'Queue', req: true, desc: 'Redis 7 connection string for Asynq task queues and rate limiters.' },
  { name: 'INROAD_MASTER_KEY', category: 'Security', req: true, desc: '32-byte hex master key for AES-256-GCM envelope encryption of mail credentials.' },
  { name: 'INROAD_JWT_SECRET', category: 'Auth', req: true, desc: 'Secret key used to sign and verify session JWT access tokens.' },
  { name: 'INROAD_PORT', category: 'Server', req: false, desc: 'HTTP port for API control plane server (default: 8080).' },
  { name: 'INROAD_MAIL_ALLOW_PRIVATE_HOSTS', category: 'Security', req: false, desc: 'Set to true in dev to bypass SSRF checks on local SMTP/IMAP servers.' },
  { name: 'INROAD_WORKER_CONCURRENCY', category: 'Worker', req: false, desc: 'Number of concurrent worker goroutines for message delivery (default: 20).' },
  { name: 'INROAD_MCP_ENABLED', category: 'MCP', req: false, desc: 'Enable Model Context Protocol /v1/mcp endpoint (default: true).' },
]

export function DocsHubPage() {
  const [activeSection, setActiveSection] = useState<DocSection>('overview')
  const [searchQuery, setSearchQuery] = useState('')
  const [copiedId, setCopiedId] = useState<string | null>(null)

  // Snippet generator state
  const [serverUrl, setServerUrl] = useState('http://localhost:8080')
  const [apiKey, setApiKey] = useState('inroad_sec_live_9a8b7c6d5e4f')
  const [selectedSnippetPlatform, setSelectedSnippetPlatform] = useState<
    'claude' | 'cursor' | 'windsurf' | 'langchain' | 'python_sdk' | 'ts_sdk'
  >('claude')

  // Tool inspector state
  const [selectedTool, setSelectedTool] = useState<ToolSchema>(MCP_TOOLS[0]!)

  const copyToClipboard = (text: string, id: string) => {
    void navigator.clipboard.writeText(text)
    setCopiedId(id)
    setTimeout(() => setCopiedId(null), 2000)
  }

  const generateSnippet = () => {
    switch (selectedSnippetPlatform) {
      case 'claude':
        return JSON.stringify(
          {
            mcpServers: {
              inroad: {
                command: 'npx',
                args: ['-y', '@modelcontextprotocol/server-sse', `${serverUrl}/v1/mcp`],
                headers: {
                  Authorization: `Bearer ${apiKey}`,
                },
              },
            },
          },
          null,
          2,
        )
      case 'cursor':
        return JSON.stringify(
          {
            mcpServers: {
              inroad: {
                url: `${serverUrl}/v1/mcp`,
                headers: {
                  Authorization: `Bearer ${apiKey}`,
                },
              },
            },
          },
          null,
          2,
        )
      case 'windsurf':
        return JSON.stringify(
          {
            mcpServers: {
              inroad_outreach: {
                serverUrl: `${serverUrl}/v1/mcp`,
                headers: {
                  Authorization: `Bearer ${apiKey}`,
                },
              },
            },
          },
          null,
          2,
        )
      case 'langchain':
        return `# Python LangChain MCP Adapter
from langchain_mcp_adapters.client import MultiServerMCPClient
import asyncio

async function main():
    async with MultiServerMCPClient({
        "inroad": {
            "url": "${serverUrl}/v1/mcp",
            "headers": {"Authorization": "Bearer ${apiKey}"}
        }
    }) as client:
        tools = client.get_tools()
        print(f"Loaded {len(tools)} Inroad MCP tools into LangChain agent.")

asyncio.run(main())`
      case 'python_sdk':
        return `# Python MCP SDK Client
import asyncio
from mcp import ClientSession
from mcp.client.sse import sse_client

async function run():
    headers = {"Authorization": "Bearer ${apiKey}"}
    async with sse_client("${serverUrl}/v1/mcp", headers=headers) as (read, write):
        async with ClientSession(read, write) as session:
            await session.initialize()
            result = await session.call_tool("inroad_list_campaigns", arguments={"limit": 5})
            print("Campaigns:", result.content)

asyncio.run(run())`
      case 'ts_sdk':
        return `// TypeScript @modelcontextprotocol/sdk Client
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { SSEClientTransport } from "@modelcontextprotocol/sdk/client/sse.js";

async function main() {
  const transport = new SSEClientTransport(
    new URL("${serverUrl}/v1/mcp"),
    { requestInit: { headers: { Authorization: "Bearer ${apiKey}" } } }
  );

  const client = new Client({ name: "inroad-ts-agent", version: "1.0.0" }, { capabilities: {} });
  await client.connect(transport);
  
  const response = await client.callTool({
    name: "inroad_list_campaigns",
    arguments: { limit: 10 }
  });
  
  console.log("Inroad Response:", response);
}

main().catch(Console.error);`
      default:
        return ''
    }
  }

  const filteredEnvVars = ENV_VARS.filter(
    (ev) =>
      ev.name.toLowerCase().includes(searchQuery.toLowerCase()) ||
      ev.desc.toLowerCase().includes(searchQuery.toLowerCase()) ||
      ev.category.toLowerCase().includes(searchQuery.toLowerCase()),
  )

  return (
    <Page>
      <PageTopbar
        eyebrow="Documentation"
        title="Documentation & MCP Integrations Hub"
        subtitle="Platform features, self-hosting guides, OAuth 2.1 authentication, and Agent MCP tools"
        actions={
          <div className="flex items-center gap-2">
            <Badge variant="outline" className="gap-1 border-primary/40 bg-primary/10 font-mono text-xs text-primary">
              <Zap className="size-3" />
              v1.25 Engine
            </Badge>
          </div>
        }
      />

      <PageBody className="flex flex-col md:flex-row">
        {/* Navigation Sidebar */}
        <aside className="w-full shrink-0 border-r border-border bg-surface/50 p-4 md:w-64">
          <div className="mb-4">
            <div className="relative">
              <Search className="absolute left-2.5 top-2.5 size-3.5 text-muted-foreground" />
              <Input
                type="text"
                placeholder="Search topics..."
                value={searchQuery}
                onChange={(e) => setSearchQuery(e.target.value)}
                className="h-8 pl-8 text-xs"
              />
            </div>
          </div>

          <nav className="flex flex-col gap-6">
            {NAV_SECTIONS.map((section) => (
              <div key={section.category}>
                <h3 className="mb-2 font-mono text-[10px] uppercase tracking-wider text-muted-foreground">
                  {section.title}
                </h3>
                <div className="flex flex-col gap-0.5">
                  {section.items.map((item) => {
                    const Icon = item.icon
                    const isActive = activeSection === item.id
                    return (
                      <button
                        key={item.id}
                        type="button"
                        onClick={() => setActiveSection(item.id)}
                        className={`flex h-8 items-center gap-2 rounded-md px-2.5 text-xs transition-colors ${
                          isActive
                            ? 'bg-primary/15 font-medium text-foreground text-primary'
                            : 'text-muted-foreground hover:bg-surface-2 hover:text-foreground'
                        }`}
                      >
                        <Icon className="size-3.5 shrink-0" />
                        <span className="truncate">{item.label}</span>
                        {isActive && <ChevronRight className="ml-auto size-3 shrink-0 text-primary" />}
                      </button>
                    )
                  })}
                </div>
              </div>
            ))}
          </nav>
        </aside>

        {/* Content Area */}
        <main className="flex-1 overflow-y-auto p-6 md:p-8">
          {/* SECTION: Overview */}
          {activeSection === 'overview' && (
            <div className="space-y-8">
              <div>
                <div className="flex items-center gap-2">
                  <Badge variant="outline">Architecture</Badge>
                  <span className="font-mono text-xs text-muted-foreground">Go 1.25 + React 19 Monorepo</span>
                </div>
                <h1 className="mt-2 text-2xl font-bold tracking-tight text-foreground">Inroad Platform Overview</h1>
                <p className="mt-2 text-sm text-muted-foreground">
                  Inroad is a high-throughput, self-hostable cold email sequencing and mailbox warmup platform. Designed
                  with a robust security model and isolation seams, it separates control plane administration from background execution workers.
                </p>
              </div>

              <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
                <div className="rounded-lg border border-border bg-surface p-4">
                  <Mail className="size-6 text-primary" />
                  <h3 className="mt-3 text-sm font-semibold text-foreground">High-Volume Outreach</h3>
                  <p className="mt-1 text-xs text-muted-foreground">
                    Multi-step campaign sequences with rate limiting, domain rotation, and timezone sending windows.
                  </p>
                </div>
                <div className="rounded-lg border border-border bg-surface p-4">
                  <Flame className="size-6 text-orange-400" />
                  <h3 className="mt-3 text-sm font-semibold text-foreground">Peer Warmup Network</h3>
                  <p className="mt-1 text-xs text-muted-foreground">
                    Isolated peer-to-peer warmup engine with HMAC header validation and auto spam-folder rescue.
                  </p>
                </div>
                <div className="rounded-lg border border-border bg-surface p-4">
                  <Gauge className="size-6 text-emerald-400" />
                  <h3 className="mt-3 text-sm font-semibold text-foreground">Deliverability Telemetry</h3>
                  <p className="mt-1 text-xs text-muted-foreground">
                    Real-time SPF, DKIM, DMARC validator, bounce rate monitoring, and spam trap protection.
                  </p>
                </div>
                <div className="rounded-lg border border-border bg-surface p-4">
                  <ShieldCheck className="size-6 text-blue-400" />
                  <h3 className="mt-3 text-sm font-semibold text-foreground">DEK / KEK Security</h3>
                  <p className="mt-1 text-xs text-muted-foreground">
                    AES-256-GCM envelope encryption protecting mailbox credentials with per-workspace DEK shredding.
                  </p>
                </div>
                <div className="rounded-lg border border-border bg-surface p-4">
                  <Cpu className="size-6 text-purple-400" />
                  <h3 className="mt-3 text-sm font-semibold text-foreground">Model Context Protocol</h3>
                  <p className="mt-1 text-xs text-muted-foreground">
                    Native MCP JSON-RPC 2.0 endpoint allowing Claude, Cursor, and custom AI agents to drive campaigns.
                  </p>
                </div>
                <div className="rounded-lg border border-border bg-surface p-4">
                  <MessageSquare className="size-6 text-cyan-400" />
                  <h3 className="mt-3 text-sm font-semibold text-foreground">Deterministic Classifier</h3>
                  <p className="mt-1 text-xs text-muted-foreground">
                    Pure offline reply header & sentiment analysis automatically managing suppression lists and OOO states.
                  </p>
                </div>
              </div>

              <div className="rounded-lg border border-border bg-surface p-5">
                <h3 className="font-mono text-xs font-semibold uppercase tracking-wider text-foreground">
                  Core Invariants & Architecture Seams
                </h3>
                <ul className="mt-3 space-y-2 text-xs text-muted-foreground">
                  <li className="flex items-start gap-2">
                    <Check className="mt-0.5 size-4 text-emerald-400 shrink-0" />
                    <span>
                      <strong className="text-foreground">Context-Derived Multi-Tenancy:</strong> Every query enforces workspace isolation derived directly from JWT claims.
                    </span>
                  </li>
                  <li className="flex items-start gap-2">
                    <Check className="mt-0.5 size-4 text-emerald-400 shrink-0" />
                    <span>
                      <strong className="text-foreground">Claim-Before-Send Idempotency:</strong> Atomic database state reservation guarantees no duplicate email delivery during retries.
                    </span>
                  </li>
                  <li className="flex items-start gap-2">
                    <Check className="mt-0.5 size-4 text-emerald-400 shrink-0" />
                    <span>
                      <strong className="text-foreground">Outbound SSRF Guard (`mail.vetAddr`):</strong> User mail server inputs are strictly filtered against RFC1918 private ranges & loopback.
                    </span>
                  </li>
                </ul>
              </div>
            </div>
          )}

          {/* SECTION: Campaigns */}
          {activeSection === 'campaigns' && (
            <div className="space-y-6">
              <div>
                <h1 className="text-2xl font-bold tracking-tight text-foreground">Campaign Sequences & Automation</h1>
                <p className="mt-1 text-sm text-muted-foreground">
                  Build and manage targeted outreach sequences with liquid tags, variant testing, and automated scheduling.
                </p>
              </div>

              <div className="rounded-lg border border-border bg-surface p-5 space-y-4">
                <h3 className="text-sm font-semibold text-foreground">Key Features</h3>
                <div className="grid gap-3 sm:grid-cols-2">
                  <div className="rounded border border-border bg-surface-2 p-3">
                    <h4 className="text-xs font-medium text-foreground">Liquid Template Variables</h4>
                    <p className="mt-1 text-xs text-muted-foreground">
                      Dynamic tags like <code className="font-mono text-primary">{"{{first_name}}"}</code>, <code className="font-mono text-primary">{"{{company}}"}</code>, and fallback defaults.
                    </p>
                  </div>
                  <div className="rounded border border-border bg-surface-2 p-3">
                    <h4 className="text-xs font-medium text-foreground">Smart Sending Windows</h4>
                    <p className="mt-1 text-xs text-muted-foreground">
                      Timezone-aware schedules matching prospect business hours with customizable daily sending caps.
                    </p>
                  </div>
                  <div className="rounded border border-border bg-surface-2 p-3">
                    <h4 className="text-xs font-medium text-foreground">Multi-Mailbox Rotation</h4>
                    <p className="mt-1 text-xs text-muted-foreground">
                      Distribute message sending across multiple domain mailboxes to preserve deliverability reputation.
                    </p>
                  </div>
                  <div className="rounded border border-border bg-surface-2 p-3">
                    <h4 className="text-xs font-medium text-foreground">Variant A/B Testing</h4>
                    <p className="mt-1 text-xs text-muted-foreground">
                      Test multiple subject lines and message bodies with automatic winning variant promotion.
                    </p>
                  </div>
                </div>
              </div>
            </div>
          )}

          {/* SECTION: Warmup */}
          {activeSection === 'warmup' && (
            <div className="space-y-6">
              <div>
                <h1 className="text-2xl font-bold tracking-tight text-foreground">Mailbox Warmup Engine</h1>
                <p className="mt-1 text-sm text-muted-foreground">
                  Automated peer-to-peer mailbox warming to maximize primary inbox placement.
                </p>
              </div>

              <div className="rounded-lg border border-border bg-surface p-5 space-y-4">
                <h3 className="text-sm font-semibold text-foreground">Warmup Mechanics</h3>
                <p className="text-xs text-muted-foreground leading-relaxed">
                  Inroad’s warmup engine establishes sending history between connected mailboxes in your network. 
                  Incoming warmup emails are stamped with constant-time HMAC headers (<code className="font-mono text-primary">X-Inroad-Warmup</code>). 
                  The inbox poller intercepts these messages, automatically marks them as important, rescues them from spam folders if needed, and files them away without cluttering user inboxes.
                </p>
              </div>
            </div>
          )}

          {/* SECTION: Deliverability */}
          {activeSection === 'deliverability' && (
            <div className="space-y-6">
              <div>
                <h1 className="text-2xl font-bold tracking-tight text-foreground">Deliverability & DNS Health</h1>
                <p className="mt-1 text-sm text-muted-foreground">
                  Comprehensive telemetry monitoring SPF, DKIM, DMARC, MX, and domain blacklist status.
                </p>
              </div>

              <div className="rounded-lg border border-border bg-surface p-5">
                <h3 className="text-sm font-semibold text-foreground">DNS Record Standard</h3>
                <div className="mt-3 overflow-x-auto">
                  <table className="w-full text-left font-mono text-xs">
                    <thead>
                      <tr className="border-b border-border text-muted-foreground">
                        <th className="pb-2">Type</th>
                        <th className="pb-2">Host / Name</th>
                        <th className="pb-2">Value / Target</th>
                      </tr>
                    </thead>
                    <tbody className="divide-y divide-border">
                      <tr>
                        <td className="py-2 text-primary font-bold">SPF</td>
                        <td className="py-2 text-foreground">@</td>
                        <td className="py-2 text-muted-foreground">v=spf1 include:inroad-mail.com ~all</td>
                      </tr>
                      <tr>
                        <td className="py-2 text-primary font-bold">DKIM</td>
                        <td className="py-2 text-foreground">inroad._domainkey</td>
                        <td className="py-2 text-muted-foreground">v=DKIM1; k=rsa; p=MIGfMA0GCSqGSIb3D...</td>
                      </tr>
                      <tr>
                        <td className="py-2 text-primary font-bold">DMARC</td>
                        <td className="py-2 text-foreground">_dmarc</td>
                        <td className="py-2 text-muted-foreground">v=DMARC1; p=quarantine; rua=mailto:dmarc@yourdomain.com</td>
                      </tr>
                    </tbody>
                  </table>
                </div>
              </div>
            </div>
          )}

          {/* SECTION: CRM */}
          {activeSection === 'crm' && (
            <div className="space-y-6">
              <div>
                <h1 className="text-2xl font-bold tracking-tight text-foreground">CRM & Deal Pipeline</h1>
                <p className="mt-1 text-sm text-muted-foreground">
                  Manage leads, track deals through customizable stage columns, and automate sales actions.
                </p>
              </div>

              <div className="rounded-lg border border-border bg-surface p-5">
                <h3 className="text-sm font-semibold text-foreground">Pipeline Features</h3>
                <ul className="mt-3 space-y-2 text-xs text-muted-foreground">
                  <li>• Drag-and-drop deal stage management (Lead, Qualified, Demo Scheduled, Closed Won).</li>
                  <li>• Automated sequence pause when a prospect moves to an active deal stage.</li>
                  <li>• Bidirectional webhook syncing with External CRMs (HubSpot, Salesforce).</li>
                </ul>
              </div>
            </div>
          )}

          {/* SECTION: Reply Classifier */}
          {activeSection === 'replyclassify' && (
            <div className="space-y-6">
              <div>
                <h1 className="text-2xl font-bold tracking-tight text-foreground">Reply Classifier & AI Compliance</h1>
                <p className="mt-1 text-sm text-muted-foreground">
                  Deterministic offline reply header & sentiment classification engine.
                </p>
              </div>

              <div className="rounded-lg border border-border bg-surface p-5 space-y-4">
                <h3 className="text-sm font-semibold text-foreground">Classification Categories</h3>
                <div className="grid gap-3 sm:grid-cols-3">
                  <div className="rounded border border-border bg-surface-2 p-3">
                    <Badge variant="outline" className="border-emerald-500/40 text-emerald-400">Interested</Badge>
                    <p className="mt-2 text-xs text-muted-foreground">Triggers deal creation & notifies owner.</p>
                  </div>
                  <div className="rounded border border-border bg-surface-2 p-3">
                    <Badge variant="outline" className="border-amber-500/40 text-amber-400">Out of Office</Badge>
                    <p className="mt-2 text-xs text-muted-foreground">Keeps campaign enrollment active; pauses sequence wait time.</p>
                  </div>
                  <div className="rounded border border-border bg-surface-2 p-3">
                    <Badge variant="outline" className="border-rose-500/40 text-rose-400">Unsubscribe</Badge>
                    <p className="mt-2 text-xs text-muted-foreground">Executes workspace-scoped suppression with ON CONFLICT DO NOTHING.</p>
                  </div>
                </div>
              </div>
            </div>
          )}

          {/* SECTION: Docker Compose */}
          {activeSection === 'docker' && (
            <div className="space-y-6">
              <div>
                <h1 className="text-2xl font-bold tracking-tight text-foreground">Single-Instance Docker Compose</h1>
                <p className="mt-1 text-sm text-muted-foreground">
                  Deploy Inroad on any VM (Ubuntu/Debian) in under 60 seconds with Docker Compose.
                </p>
              </div>

              <div className="relative rounded-lg border border-border bg-surface-2 p-4 font-mono text-xs">
                <button
                  type="button"
                  onClick={() =>
                    copyToClipboard(
                      `version: '3.8'\n\nservices:\n  inroad-api:\n    image: ghcr.io/inroad/inroad-api:latest\n    ports:\n      - "8080:8080"\n    environment:\n      - INROAD_DATABASE_URL=postgres://inroad:pass@postgres:5432/inroad?sslmode=disable\n      - INROAD_REDIS_URL=redis://redis:6379/0\n      - INROAD_MASTER_KEY=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n    depends_on:\n      - postgres\n      - redis\n\n  inroad-worker:\n    image: ghcr.io/inroad/inroad-worker:latest\n    environment:\n      - INROAD_DATABASE_URL=postgres://inroad:pass@postgres:5432/inroad?sslmode=disable\n      - INROAD_REDIS_URL=redis://redis:6379/0\n      - INROAD_MASTER_KEY=0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n    depends_on:\n      - postgres\n      - redis\n\n  postgres:\n    image: postgres:16-alpine\n    environment:\n      POSTGRES_USER: inroad\n      POSTGRES_PASSWORD: pass\n      POSTGRES_DB: inroad\n\n  redis:\n    image: redis:7-alpine`,
                      'docker-yaml',
                    )
                  }
                  className="absolute right-3 top-3 flex items-center gap-1 rounded bg-surface border border-border px-2 py-1 text-[10px] text-muted-foreground hover:text-foreground"
                >
                  {copiedId === 'docker-yaml' ? <Check className="size-3 text-emerald-400" /> : <Copy className="size-3" />}
                  {copiedId === 'docker-yaml' ? 'Copied!' : 'Copy docker-compose.yml'}
                </button>
                <pre className="overflow-x-auto text-muted-foreground">
                  {`version: '3.8'

services:
  inroad-api:
    image: ghcr.io/inroad/inroad-api:latest
    ports:
      - "8080:8080"
    environment:
      - INROAD_DATABASE_URL=postgres://inroad:pass@postgres:5432/inroad?sslmode=disable
      - INROAD_REDIS_URL=redis://redis:6379/0
      - INROAD_MASTER_KEY=0123456789abcdef0123456789abcdef...
    depends_on:
      - postgres
      - redis

  inroad-worker:
    image: ghcr.io/inroad/inroad-worker:latest
    depends_on:
      - postgres
      - redis

  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: inroad
      POSTGRES_PASSWORD: pass
      POSTGRES_DB: inroad

  redis:
    image: redis:7-alpine`}
                </pre>
              </div>
            </div>
          )}

          {/* SECTION: Terraform AWS */}
          {activeSection === 'terraform' && (
            <div className="space-y-6">
              <div>
                <h1 className="text-2xl font-bold tracking-tight text-foreground">AWS ECS + RDS + S3 Terraform Architecture</h1>
                <p className="mt-1 text-sm text-muted-foreground">
                  Production-grade infrastructure for AWS using Amazon ECS Fargate, Multi-AZ RDS PostgreSQL, and S3.
                </p>
              </div>

              <div className="rounded-lg border border-border bg-surface p-5 space-y-3">
                <h3 className="text-sm font-semibold text-foreground">Infrastructure Components</h3>
                <div className="grid gap-3 sm:grid-cols-2 text-xs text-muted-foreground">
                  <div className="rounded border border-border p-3">
                    <strong className="text-foreground block mb-1">ECS Fargate Cluster</strong>
                    Runs control plane API tasks behind ALB and worker tasks in auto-scaling ECS service.
                  </div>
                  <div className="rounded border border-border p-3">
                    <strong className="text-foreground block mb-1">Multi-AZ RDS PostgreSQL</strong>
                    Encrypted Postgres 16 database instance with daily automated backups.
                  </div>
                  <div className="rounded border border-border p-3">
                    <strong className="text-foreground block mb-1">ElastiCache Redis</strong>
                    Managed Redis cluster powering Asynq queues and rate limiters.
                  </div>
                  <div className="rounded border border-border p-3">
                    <strong className="text-foreground block mb-1">KMS & S3 Bucket</strong>
                    Private S3 bucket for assets with AWS KMS Envelope encryption.
                  </div>
                </div>
              </div>
            </div>
          )}

          {/* SECTION: Helm Kubernetes */}
          {activeSection === 'helm' && (
            <div className="space-y-6">
              <div>
                <h1 className="text-2xl font-bold tracking-tight text-foreground">Kubernetes & Helm Deployment</h1>
                <p className="mt-1 text-sm text-muted-foreground">
                  Deploy to EKS, GKE, or any Kubernetes cluster using Helm charts.
                </p>
              </div>

              <div className="rounded-lg border border-border bg-surface p-4 font-mono text-xs">
                <pre className="text-muted-foreground">
                  {`# Add Helm repo & install
helm repo add inroad https://charts.inroad.io
helm repo update

helm install inroad inroad/inroad \\
  --namespace inroad-prod --create-namespace \\
  --set env.databaseUrl="postgres://..." \\
  --set env.masterKey="..."`}
                </pre>
              </div>
            </div>
          )}

          {/* SECTION: Environment Variables */}
          {activeSection === 'envvars' && (
            <div className="space-y-6">
              <div>
                <h1 className="text-2xl font-bold tracking-tight text-foreground">Environment Variables Reference</h1>
                <p className="mt-1 text-sm text-muted-foreground">
                  Complete environment variable options for configuring control plane and worker services.
                </p>
              </div>

              <div className="rounded-lg border border-border bg-surface overflow-hidden">
                <table className="w-full text-left text-xs">
                  <thead>
                    <tr className="border-b border-border bg-surface-2 font-mono text-muted-foreground">
                      <th className="p-3">Variable Name</th>
                      <th className="p-3">Category</th>
                      <th className="p-3">Required</th>
                      <th className="p-3">Description</th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-border">
                    {filteredEnvVars.map((ev) => (
                      <tr key={ev.name} className="hover:bg-surface-2/50">
                        <td className="p-3 font-mono font-medium text-primary">{ev.name}</td>
                        <td className="p-3">
                          <Badge variant="outline" className="text-[10px]">{ev.category}</Badge>
                        </td>
                        <td className="p-3">
                          {ev.req ? (
                            <span className="text-rose-400 font-semibold">Yes</span>
                          ) : (
                            <span className="text-muted-foreground">Optional</span>
                          )}
                        </td>
                        <td className="p-3 text-muted-foreground">{ev.desc}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )}

          {/* SECTION: MCP Overview */}
          {activeSection === 'mcp-overview' && (
            <div className="space-y-6">
              <div>
                <h1 className="text-2xl font-bold tracking-tight text-foreground">Model Context Protocol (MCP) Hub</h1>
                <p className="mt-1 text-sm text-muted-foreground">
                  Expose Inroad’s outreach capabilities directly to AI agents via the standard Model Context Protocol.
                </p>
              </div>

              <div className="rounded-lg border border-border bg-surface p-5 space-y-4">
                <h3 className="text-sm font-semibold text-foreground">Endpoint Architecture</h3>
                <p className="text-xs text-muted-foreground leading-relaxed">
                  Inroad serves a JSON-RPC 2.0 endpoint at <code className="font-mono text-primary">/v1/mcp</code> over Server-Sent Events (SSE) and HTTP Streaming. 
                  AI models like Claude, GPT-4, and local LLMs can query campaigns, list mailboxes, analyze deliverability, and trigger enrollments dynamically.
                </p>
              </div>
            </div>
          )}

          {/* SECTION: MCP OAuth */}
          {activeSection === 'mcp-oauth' && (
            <div className="space-y-6">
              <div>
                <h1 className="text-2xl font-bold tracking-tight text-foreground">OAuth 2.1 Security & PKCE</h1>
                <p className="mt-1 text-sm text-muted-foreground">
                  Secure agent authorization flow adhering to OAuth 2.1 specifications with PKCE challenge verification.
                </p>
              </div>

              <div className="rounded-lg border border-border bg-surface p-5 space-y-3 text-xs text-muted-foreground">
                <div className="flex items-center gap-2">
                  <Badge variant="outline" className="border-blue-500/40 text-blue-400">Step 1</Badge>
                  <span>Agent initiates authorization code grant at <code className="font-mono text-foreground">/oauth2/authorize</code> with code_challenge.</span>
                </div>
                <div className="flex items-center gap-2">
                  <Badge variant="outline" className="border-blue-500/40 text-blue-400">Step 2</Badge>
                  <span>User grants workspace permissions on consent screen.</span>
                </div>
                <div className="flex items-center gap-2">
                  <Badge variant="outline" className="border-blue-500/40 text-blue-400">Step 3</Badge>
                  <span>Agent exchanges code at <code className="font-mono text-foreground">/oauth2/token</code> for Bearer access token.</span>
                </div>
              </div>
            </div>
          )}

          {/* SECTION: MCP Inspector */}
          {activeSection === 'mcp-inspector' && (
            <div className="space-y-6">
              <div>
                <h1 className="text-2xl font-bold tracking-tight text-foreground">Interactive Tool Schema Inspector</h1>
                <p className="mt-1 text-sm text-muted-foreground">
                  Inspect available MCP tools, parameters, descriptions, and example JSON response schemas.
                </p>
              </div>

              <div className="grid gap-6 lg:grid-cols-3">
                {/* Tool Selector List */}
                <div className="rounded-lg border border-border bg-surface p-3 space-y-1">
                  <h3 className="px-2 py-1 font-mono text-[10px] uppercase text-muted-foreground">Available MCP Tools</h3>
                  {MCP_TOOLS.map((t) => (
                    <button
                      key={t.name}
                      type="button"
                      onClick={() => setSelectedTool(t)}
                      className={`w-full text-left rounded-md px-3 py-2 text-xs transition-colors ${
                        selectedTool.name === t.name
                          ? 'bg-primary/15 font-semibold text-primary'
                          : 'hover:bg-surface-2 text-muted-foreground'
                      }`}
                    >
                      <div className="font-mono text-foreground">{t.name}</div>
                      <div className="truncate text-[11px] text-muted-foreground/80">{t.description}</div>
                    </button>
                  ))}
                </div>

                {/* Tool Details Inspector */}
                <div className="lg:col-span-2 space-y-4">
                  <div className="rounded-lg border border-border bg-surface p-5 space-y-4">
                    <div>
                      <div className="flex items-center gap-2">
                        <Badge variant="outline" className="border-primary/40 font-mono text-primary">
                          Tool Schema
                        </Badge>
                        <h2 className="font-mono text-lg font-bold text-foreground">{selectedTool.name}</h2>
                      </div>
                      <p className="mt-1 text-xs text-muted-foreground">{selectedTool.description}</p>
                    </div>

                    <div>
                      <h4 className="font-mono text-xs font-semibold uppercase text-muted-foreground mb-2">Input Parameters</h4>
                      <div className="space-y-2">
                        {Object.entries(selectedTool.parameters).map(([paramName, paramInfo]) => (
                          <div key={paramName} className="rounded border border-border bg-surface-2 p-2.5 text-xs">
                            <div className="flex items-center justify-between">
                              <span className="font-mono font-semibold text-foreground">{paramName}</span>
                              <span className="font-mono text-[10px] text-muted-foreground">
                                {paramInfo.type} {paramInfo.required && <span className="text-rose-400 font-bold">*required</span>}
                              </span>
                            </div>
                            <p className="mt-1 text-[11px] text-muted-foreground">{paramInfo.description}</p>
                          </div>
                        ))}
                      </div>
                    </div>

                    <div>
                      <h4 className="font-mono text-xs font-semibold uppercase text-muted-foreground mb-2">Example Output Payload</h4>
                      <pre className="rounded border border-border bg-surface-2 p-3 font-mono text-xs text-emerald-400 overflow-x-auto">
                        {JSON.stringify(selectedTool.exampleOutput, null, 2)}
                      </pre>
                    </div>
                  </div>
                </div>
              </div>
            </div>
          )}

          {/* SECTION: Snippet Generator */}
          {activeSection === 'mcp-snippets' && (
            <div className="space-y-6">
              <div>
                <h1 className="text-2xl font-bold tracking-tight text-foreground">One-Click Snippet Generator</h1>
                <p className="mt-1 text-sm text-muted-foreground">
                  Generate copy-paste configuration files for Claude Desktop, Cursor, Windsurf, LangChain, and SDKs.
                </p>
              </div>

              <div className="rounded-lg border border-border bg-surface p-5 space-y-4">
                <div className="grid gap-4 sm:grid-cols-2">
                  <div>
                    <label htmlFor="mcp-server-url" className="block text-xs font-medium text-foreground mb-1">
                      Inroad Server URL
                    </label>
                    <Input
                      id="mcp-server-url"
                      type="text"
                      value={serverUrl}
                      onChange={(e) => setServerUrl(e.target.value)}
                      className="h-8 text-xs font-mono"
                    />
                  </div>
                  <div>
                    <label htmlFor="mcp-api-key" className="block text-xs font-medium text-foreground mb-1">
                      API Key / Bearer Token
                    </label>
                    <Input
                      id="mcp-api-key"
                      type="text"
                      value={apiKey}
                      onChange={(e) => setApiKey(e.target.value)}
                      className="h-8 text-xs font-mono"
                    />
                  </div>
                </div>

                {/* Target Platform Tabs */}
                <div className="flex flex-wrap gap-2 border-b border-border pb-3">
                  {[
                    { id: 'claude', label: 'Claude Desktop' },
                    { id: 'cursor', label: 'Cursor (.mcp.json)' },
                    { id: 'windsurf', label: 'Windsurf' },
                    { id: 'langchain', label: 'LangChain' },
                    { id: 'python_sdk', label: 'Python SDK' },
                    { id: 'ts_sdk', label: 'TypeScript SDK' },
                  ].map((tab) => (
                    <button
                      key={tab.id}
                      type="button"
                      onClick={() => setSelectedSnippetPlatform(tab.id as typeof selectedSnippetPlatform)}
                      className={`rounded px-3 py-1 text-xs font-medium transition-colors ${
                        selectedSnippetPlatform === tab.id
                          ? 'bg-primary text-primary-foreground'
                          : 'bg-surface-2 text-muted-foreground hover:text-foreground'
                      }`}
                    >
                      {tab.label}
                    </button>
                  ))}
                </div>

                {/* Snippet Code Box */}
                <div className="relative rounded-lg border border-border bg-surface-2 p-4 font-mono text-xs">
                  <Button
                    size="sm"
                    variant="outline"
                    onClick={() => copyToClipboard(generateSnippet(), 'snippet-code')}
                    className="absolute right-3 top-3 h-7 gap-1.5 text-xs"
                  >
                    {copiedId === 'snippet-code' ? <Check className="size-3 text-emerald-400" /> : <Copy className="size-3" />}
                    {copiedId === 'snippet-code' ? 'Copied!' : 'Copy Code'}
                  </Button>
                  <pre className="overflow-x-auto pt-6 text-foreground">{generateSnippet()}</pre>
                </div>
              </div>
            </div>
          )}
        </main>
      </PageBody>
    </Page>
  )
}
