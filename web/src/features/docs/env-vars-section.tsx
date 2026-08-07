import { SectionBar } from '@/components/layout/page'
import { ENV_GROUPS } from './docs-data'

/**
 * A curated configuration reference (see docs-data.ts for the source-of-truth
 * files it is transcribed from). The full set lives in .env.example and the
 * environment-variables deploy guide.
 */
export function EnvVarsSection() {
  return (
    <section aria-labelledby="docs-env-vars">
      <SectionBar label="Configuration" />
      <div className="border-b border-border px-4 py-5 sm:px-6">
        <h2 id="docs-env-vars" className="sr-only">
          Environment variables
        </h2>
        <p className="max-w-2xl text-sm text-muted-foreground">
          All configuration is environment variables prefixed{' '}
          <code className="font-mono text-[12px] text-foreground">INROAD_</code>. The variables below are the
          decisions a self-hoster actually makes; <code className="font-mono text-[12px] text-foreground">.env.example</code>{' '}
          in the repo documents every one with its default.
        </p>
      </div>
      {ENV_GROUPS.map((group) => (
        <div key={group.title}>
          <div className="border-b border-border bg-surface-2/60 px-5 py-1.5 font-mono text-[10px] uppercase tracking-[0.14em] text-faint">
            {group.title}
          </div>
          {group.vars.map((v) => (
            <div key={v.name} className="flex flex-col gap-1 border-b border-border px-5 py-2.5 sm:flex-row sm:items-baseline sm:gap-4">
              <code className="w-72 shrink-0 font-mono text-[12px] text-foreground">{v.name}</code>
              <span className="min-w-0 flex-1 text-[12.5px] text-muted-foreground">
                {v.description}
                {v.example && (
                  <code className="ml-2 rounded bg-surface-2 px-1.5 py-0.5 font-mono text-[11px] text-faint">
                    {v.example}
                  </code>
                )}
              </span>
            </div>
          ))}
        </div>
      ))}
    </section>
  )
}
