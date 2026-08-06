import DOMPurify from 'dompurify'
import { useMemo } from 'react'

/**
 * The one place a received message's body reaches the DOM. HTML from an
 * inbound reply is attacker-controlled — an external sender fully composes
 * it — so it is sanitized here, never trusted, and never rendered anywhere
 * else via dangerouslySetInnerHTML.
 */
export function MessageBody({ bodyText, bodyHtml }: { bodyText: string; bodyHtml: string }) {
  const safeHtml = useMemo(() => (bodyHtml ? DOMPurify.sanitize(bodyHtml) : ''), [bodyHtml])
  if (safeHtml) {
    return <div className="prose prose-sm max-w-none text-foreground" dangerouslySetInnerHTML={{ __html: safeHtml }} />
  }
  return <p className="whitespace-pre-wrap text-sm text-foreground">{bodyText}</p>
}
