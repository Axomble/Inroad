import ReactMarkdown, { type Components } from 'react-markdown'
import remarkGfm from 'remark-gfm'

const referencePattern = /\[\[([a-z_]+):([0-9a-f-]{36}):([^\]]+)\]\]/gi

interface SafeLink {
  href: string
  /** True only when the resolved URL is same-origin — a protocol-relative
   * `//evil.com` also "starts with /", so the path prefix cannot be trusted to
   * decide whether a model-emitted link stays in the tab. */
  internal: boolean
}

function safeLink(value: string | undefined): SafeLink | undefined {
  if (!value) return undefined
  try {
    const url = new URL(value, window.location.origin)
    if (!['http:', 'https:', 'mailto:'].includes(url.protocol)) return undefined
    return { href: value, internal: url.protocol !== 'mailto:' && url.origin === window.location.origin }
  } catch {
    return undefined
  }
}

function recordHref(type: string, id: string): string | undefined {
  switch (type) {
    case 'campaign':
      return `/app/campaigns/${id}`
    case 'contact':
      return `/app/contacts/${id}`
    case 'list':
      return `/app/contacts?list=${id}`
    case 'mailbox':
      return `/app/mailboxes?mailbox=${id}`
    default:
      return undefined
  }
}

const components: Components = {
  a: ({ href, children }) => {
    const safe = safeLink(href)
    return safe ? (
      <a
        href={safe.href}
        target={safe.internal ? undefined : '_blank'}
        rel={safe.internal ? undefined : 'noopener noreferrer'}
        className="font-medium text-accent-ink underline decoration-accent-ink/35 underline-offset-2"
      >
        {children}
      </a>
    ) : (
      <span>{children}</span>
    )
  },
  code: ({ children }) => (
    <code className="rounded bg-surface-2 px-1 py-0.5 font-mono text-[0.88em]">{children}</code>
  ),
}

function MarkdownBlock({ children }: { children: string }) {
  return (
    <ReactMarkdown remarkPlugins={[remarkGfm]} components={components}>
      {children}
    </ReactMarkdown>
  )
}

export function AgentMarkdown({ text }: { text: string }) {
  const pieces: React.ReactNode[] = []
  let cursor = 0
  for (const match of text.matchAll(referencePattern)) {
    const [, rawType, id, label] = match
    if (!rawType || !id || !label) continue
    const index = match.index ?? 0
    if (index > cursor) {
      pieces.push(<MarkdownBlock key={`text-${cursor}`}>{text.slice(cursor, index)}</MarkdownBlock>)
    }
    const href = recordHref(rawType.toLowerCase(), id)
    pieces.push(
      href ? (
        <a
          key={`ref-${index}`}
          href={href}
          className="mx-0.5 inline-flex items-center rounded-full border border-border-strong bg-surface-2 px-2 py-0.5 text-[11px] font-semibold text-foreground hover:border-primary"
        >
          {label}
        </a>
      ) : (
        <span key={`ref-${index}`}>{label}</span>
      ),
    )
    cursor = index + match[0].length
  }
  if (cursor < text.length) pieces.push(<MarkdownBlock key={`text-${cursor}`}>{text.slice(cursor)}</MarkdownBlock>)
  return (
    <div className="agent-markdown space-y-2 text-[13px] leading-6 text-foreground">
      {pieces}
    </div>
  )
}
