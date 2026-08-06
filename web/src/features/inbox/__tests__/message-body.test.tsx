import { render, screen } from '@testing-library/react'
import { MessageBody } from '../message-body'

test('strips a script tag from untrusted HTML before rendering', () => {
  render(<MessageBody bodyText="" bodyHtml="<p>hi</p><script>window.__pwned = true</script>" />)
  expect(screen.getByText('hi')).toBeInTheDocument()
  expect(document.querySelector('script')).not.toBeInTheDocument()
})

test('strips an inline event handler attribute', () => {
  render(<MessageBody bodyText="" bodyHtml={'<img src="x.png" onerror="window.__pwned=true">'} />)
  expect(document.querySelector('img')?.getAttribute('onerror')).toBeNull()
})

test('falls back to plain text when there is no HTML part', () => {
  render(<MessageBody bodyText="Just plain text, thanks" bodyHtml="" />)
  expect(screen.getByText('Just plain text, thanks')).toBeInTheDocument()
})
