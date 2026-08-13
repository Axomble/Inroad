/**
 * Frontend runtime configuration — the single place Vite env vars are read.
 * Everything else imports from here, so what's configurable (and its default)
 * is one greppable list instead of `import.meta.env` reads scattered per file.
 */
export const config = {
  /** API origin+prefix. Default = same-origin, which dev proxies and prod serves. */
  apiBaseUrl: import.meta.env.VITE_API_BASE_URL ?? '/api/v1',
  /**
   * Where every "Docs & MCP" link goes. The manuals are the Astro/Starlight
   * site under docs/ — a separate site, never rendered inside the SPA. The dev
   * compose serves it on :4321 and sets VITE_DOCS_URL; a deployment that hosts
   * the built site sets it at build time. The fallback is the docs source on
   * GitHub, which renders the same markdown and always exists.
   */
  docsUrl: import.meta.env.VITE_DOCS_URL ?? 'https://github.com/Axomble/Inroad/tree/main/docs/src/content/docs',
} as const
