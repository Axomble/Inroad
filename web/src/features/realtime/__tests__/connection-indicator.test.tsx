import { screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { ConnectionIndicatorView } from '../connection-indicator'
import { initialRealtimeState, type RealtimeState } from '../socket-events'

function view(overrides: Partial<RealtimeState> = {}) {
  return renderWithProviders(<ConnectionIndicatorView {...initialRealtimeState} {...overrides} />)
}

describe('ConnectionIndicator', () => {
  it('renders nothing before a connection is attempted', () => {
    const { container } = view({ status: 'idle' })
    expect(container).toBeEmptyDOMElement()
  })

  it('renders nothing on a healthy live connection', () => {
    const { container } = view({ status: 'live', lastSeq: 12 })
    expect(container).toBeEmptyDOMElement()
  })

  it('labels the connecting state in text, not color alone', () => {
    view({ status: 'connecting' })
    expect(screen.getByText('Connecting')).toBeInTheDocument()
  })

  it('warns while reconnecting', () => {
    view({ status: 'reconnecting', attempt: 2 })
    expect(screen.getByText('Reconnecting')).toBeInTheDocument()
    expect(screen.getByRole('button')).toHaveAccessibleName(/Reconnecting/)
  })

  it('shows the terminal reason verbatim when offline', () => {
    view({ status: 'offline', error: 'Your session expired. Reload the page to resume live updates.' })
    expect(screen.getByText('Offline')).toBeInTheDocument()
    expect(screen.getByRole('button')).toHaveAccessibleName(/Your session expired/)
  })

  it('falls back to generic offline copy when no reason was given', () => {
    view({ status: 'offline', error: null })
    expect(screen.getByRole('button')).toHaveAccessibleName(/Live updates have stopped/)
  })

  it('flags a missed replay window even though the socket is live', () => {
    view({ status: 'live', lastSeq: 90, gapDetected: true })
    expect(screen.getByText('Catching up')).toBeInTheDocument()
    expect(screen.getByRole('button')).toHaveAccessibleName(/Some updates were missed/)
  })
})
