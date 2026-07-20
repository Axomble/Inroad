import { Component, type ErrorInfo, type ReactNode } from 'react'

/**
 * Top-level React error boundary wrapped around the Provider/PersistGate tree
 * in `main.tsx`. Router-level errors are handled by the router's
 * `defaultErrorComponent`; this catches everything else (a render throw in a
 * provider, a lazy chunk load failure) so the whole tab doesn't go blank.
 */
interface Props {
  children: ReactNode
  fallback?: (error: Error, reset: () => void) => ReactNode
}
interface State {
  error: Error | null
}

export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null }

  static getDerivedStateFromError(error: Error): State {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    // Console log is intentional — this is a last-resort surface, and losing
    // the trace would make prod debugging harder. Wire to a real reporter later.
    // eslint-disable-next-line no-console
    console.error('ErrorBoundary caught:', error, info.componentStack)
  }

  reset = (): void => {
    this.setState({ error: null })
  }

  render(): ReactNode {
    if (this.state.error) {
      if (this.props.fallback) return this.props.fallback(this.state.error, this.reset)
      return (
        <div className="flex h-dvh flex-col items-center justify-center gap-3 p-6 text-center">
          <p className="font-mono text-[11px] uppercase tracking-[0.16em] text-danger">Fatal</p>
          <h1 className="text-lg font-semibold tracking-tight text-foreground">
            Something went wrong.
          </h1>
          <p className="max-w-sm text-xs text-muted-foreground">{this.state.error.message}</p>
          <button
            type="button"
            className="mt-2 rounded-md border border-border-strong bg-surface-2 px-3 py-1.5 text-sm text-foreground transition-colors hover:bg-surface"
            onClick={this.reset}
          >
            Try again
          </button>
        </div>
      )
    }
    return this.props.children
  }
}
