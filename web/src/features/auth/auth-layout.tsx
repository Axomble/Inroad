import { AuthShowcase } from './auth-showcase'

/**
 * Split-screen auth shell. Left: brand + form (the operator does the work here).
 * Right (lg+): the product's thesis — an animated warmup/inbox visualization
 * with the value proposition. The right pane is decorative and hidden on small
 * screens, where the form takes the full width.
 */
export function AuthLayout({ children }: { children: React.ReactNode }) {
  return (
    <div className="grid min-h-dvh lg:grid-cols-[1fr_1.05fr]">
      {/* form column */}
      <div className="relative flex min-h-dvh flex-col px-6 py-8 sm:px-10">
        <div className="auth-rise flex items-center gap-2" style={{ animationDelay: '40ms' }}>
          <div className="grid size-8 place-items-center rounded-lg bg-primary text-sm font-bold text-primary-foreground shadow-[inset_0_1px_0_rgba(255,255,255,0.25),0_2px_0_var(--primary-edge)]">
            I
          </div>
          <span className="text-[15px] font-bold tracking-tight">Inroad</span>
        </div>

        <div className="flex flex-1 items-center justify-center py-10">
          <div className="w-full max-w-sm">{children}</div>
        </div>

        <p className="auth-rise text-center text-xs text-faint sm:text-left" style={{ animationDelay: '360ms' }}>
          © Inroad · Self-hosted cold email &amp; warmup
        </p>
      </div>

      {/* showcase column */}
      <div className="relative hidden overflow-hidden border-l border-border lg:block">
        <AuthShowcase />
        {/* top + bottom vignette to seat the overlay copy */}
        <div
          aria-hidden="true"
          className="pointer-events-none absolute inset-0 [background:linear-gradient(to_bottom,rgba(15,11,22,0.55),transparent_28%,transparent_60%,rgba(15,11,22,0.85))]"
        />
        <div className="relative z-10 flex h-full flex-col justify-between p-12">
          <div className="flex items-center gap-2 font-mono text-[10.5px] uppercase tracking-[0.18em] text-warm">
            <span className="size-1.5 rounded-full bg-warm warm-pulse" />
            Mailbox warmup · live
          </div>

          <div className="max-w-md">
            <h2 className="text-3xl font-semibold leading-tight tracking-tight text-foreground text-balance">
              Land in the inbox,
              <br />
              not in spam.
            </h2>
            <p className="mt-3 text-sm leading-relaxed text-muted-foreground">
              Inroad warms your mailboxes on a natural ramp and sequences cold outreach that actually gets
              delivered — all self-hosted, all under your control.
            </p>

            <dl className="mt-8 grid grid-cols-3 gap-px overflow-hidden rounded-lg border border-border bg-border">
              {[
                { v: '98.6%', l: 'Inbox placement' },
                { v: '12', l: 'Mailboxes warming' },
                { v: '3,480', l: 'Sent today' },
              ].map((s) => (
                <div key={s.l} className="bg-background/60 px-4 py-3 backdrop-blur-sm">
                  <dt className="text-[22px] font-light tabular-nums text-foreground">{s.v}</dt>
                  <dd className="mt-0.5 font-mono text-[10px] uppercase tracking-[0.12em] text-faint">{s.l}</dd>
                </div>
              ))}
            </dl>
          </div>

          <div />
        </div>
      </div>
    </div>
  )
}
