import { ArrowUpRight } from 'lucide-react'
import { SectionBar } from '@/components/layout/page'
import { GUIDE_GROUPS } from './docs-data'

/**
 * Links to the Starlight docs site source (docs/src/content/docs). There is no
 * hosted docs URL configured anywhere in the repo yet, so these point at the
 * markdown on GitHub — the repo the docs site's own config names.
 */
export function GuidesSection() {
  return (
    <section aria-labelledby="docs-guides">
      <SectionBar label="Guides" />
      <h2 id="docs-guides" className="sr-only">
        Guides
      </h2>
      {GUIDE_GROUPS.map((group) => (
        <div key={group.title}>
          <div className="border-b border-border bg-surface-2/60 px-5 py-1.5 font-mono text-[10px] uppercase tracking-[0.14em] text-faint">
            {group.title}
          </div>
          <div className="grid border-b border-border sm:grid-cols-2 lg:grid-cols-3">
            {group.guides.map((guide) => (
              <a
                key={guide.href}
                href={guide.href}
                target="_blank"
                rel="noreferrer"
                className="group flex flex-col gap-0.5 border-b border-r border-border px-5 py-3.5 transition-colors last:border-b-0 hover:bg-surface focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring sm:border-b-0"
              >
                <span className="flex items-center gap-1 text-[13.5px] font-medium text-foreground">
                  {guide.title}
                  <ArrowUpRight
                    className="size-3.5 text-faint transition-colors group-hover:text-foreground"
                    aria-hidden="true"
                  />
                </span>
                <span className="text-[12.5px] text-muted-foreground">{guide.description}</span>
              </a>
            ))}
          </div>
        </div>
      ))}
    </section>
  )
}
