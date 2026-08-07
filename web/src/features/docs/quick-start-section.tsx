import { SectionBar } from '@/components/layout/page'

/**
 * The one-command dev quick start, verified against the repo's own
 * deploy/compose/docker-compose.dev.yml and the README/CLAUDE.md dev docs.
 */
export function QuickStartSection() {
  return (
    <section aria-labelledby="docs-quick-start">
      <SectionBar label="Quick start" />
      <div className="border-b border-border px-4 py-5 sm:px-6">
        <h2 id="docs-quick-start" className="sr-only">
          Quick start
        </h2>
        <p className="max-w-2xl text-sm text-muted-foreground">
          One command brings up Postgres, Redis, migrations, the API (
          <code className="font-mono text-[12px] text-foreground">:8080</code>), the worker, and the SPA (
          <code className="font-mono text-[12px] text-foreground">:5173</code>) — no local Go or Node toolchain
          needed. Dev secrets are baked into the compose file.
        </p>
        <pre className="mt-3 max-w-2xl overflow-x-auto rounded-md border border-border bg-surface-2 px-4 py-3">
          <code className="font-mono text-[12.5px] text-foreground">
            docker compose -f deploy/compose/docker-compose.dev.yml up
          </code>
        </pre>
        <p className="mt-3 max-w-2xl text-sm text-muted-foreground">
          Go services rebuild via air and the SPA hot-reloads via Vite, both watching the bind-mounted source
          tree. For a production deployment, start from the Docker Compose guide below.
        </p>
      </div>
    </section>
  )
}
