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

  it('treats a protocol-relative link as external even though it starts with a slash', () => {
    render(<AgentMarkdown text={'[offsite](//evil.example.com/x)'} />)

    const link = screen.getByRole('link', { name: 'offsite' })
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
  })

  it('keeps a genuine in-app path in the same tab', () => {
    render(<AgentMarkdown text={'[mailboxes](/app/mailboxes)'} />)

    const link = screen.getByRole('link', { name: 'mailboxes' })
    expect(link).not.toHaveAttribute('target')
    expect(link).not.toHaveAttribute('rel')
  })

  it('does not make unsafe protocols clickable', () => {
    render(<AgentMarkdown text={'[unsafe](javascript:alert(1))'} />)
    expect(screen.queryByRole('link')).not.toBeInTheDocument()
    expect(screen.getByText('unsafe')).toBeInTheDocument()
  })
})
