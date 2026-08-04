import { render, screen } from '@testing-library/react'
import { AgentMarkdown } from './markdown'

describe('AgentMarkdown', () => {
  it('renders record references as internal chips and external links safely', () => {
    render(
      <AgentMarkdown text={'Review [[campaign:123e4567-e89b-12d3-a456-426614174000:August launch]] and [docs](https://example.com).'} />,
    )

    expect(screen.getByRole('link', { name: 'August launch' })).toHaveAttribute(
      'href',
      '/app/campaigns/123e4567-e89b-12d3-a456-426614174000',
    )
    expect(screen.getByRole('link', { name: 'docs' })).toHaveAttribute('rel', 'noopener noreferrer')
  })

  it('does not make unsafe protocols clickable', () => {
    render(<AgentMarkdown text={'[unsafe](javascript:alert(1))'} />)
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
    expect(screen.getByText('unsafe')).toBeInTheDocument()
  })
})
