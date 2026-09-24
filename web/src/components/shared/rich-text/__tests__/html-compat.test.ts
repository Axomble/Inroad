import { describe, expect, test } from 'vitest'
import { unkeptFormatting } from '../html-compat'

// An email body written outside the editor (API clients, imports) may carry
// markup the editor's schema has no place for. Loading it into the editor and
// saving would silently drop it, so the editor must be able to tell first.

/** The kind of HTML an agent or API client plausibly writes: a table of options, inline styles, a heading. */
const AGENT_AUTHORED = `<h2 style="margin:0">Quick idea for {{company}}</h2>
<p>Hi {{first_name}}, here's how teams like yours compare:</p>
<table style="border-collapse:collapse">
  <tr><th>Plan</th><th>Seats</th></tr>
  <tr><td>Starter</td><td>5</td></tr>
</table>
<p>Worth a chat?</p>`

describe('HTML the editor keeps losslessly', () => {
  test.each([
    ['empty', ''],
    ['the demo seed’s paragraph HTML', '<p>Hi {{first_name}},<br>quick one.</p><p>Thanks</p>'],
    ['the editor’s own output', '<p>Hi <strong>{{first_name}}</strong>, <em>see</em> <a href="https://x.dev">this</a></p><ul><li><p>a</p></li></ul><ol start="3"><li><p>b</p></li></ol>'],
    ['b / i spellings of bold and italic', '<p><b>bold</b> <i>italic</i></p>'],
    ['link attributes the Link extension keeps', '<p><a href="https://x.dev" target="_blank" rel="noopener" title="x" class="cta">x</a></p>'],
    ['a document wrapper with nothing in it', '<html><body><p>Hello</p></body></html>'],
    ['bare text with no paragraph', 'Hello {{first_name}}'],
    ['variable elements the editor writes on copy', '<p><span data-variable="first_name"></span></p>'],
  ])('%s', (_name, html) => {
    expect(unkeptFormatting(html)).toEqual([])
  })
})

describe('HTML the editor would lose', () => {
  test('an agent-authored body with a heading, a table and inline styles names all three', () => {
    expect(unkeptFormatting(AGENT_AUTHORED)).toEqual(['headings', 'inline styles', 'tables'])
  })

  test.each([
    ['<p style="color:red">x</p>', ['inline styles']],
    ['<style>p{color:red}</style><p>x</p>', ['style sheets']],
    ['<div>line</div>', ['layout blocks']],
    ['<p><img src="https://x.dev/a.png" alt=""></p>', ['images']],
    ['<!--[if mso]><p>Outlook</p><![endif]--><p>x</p>', ['HTML comments']],
    ['<p class="lead">x</p>', ['CSS classes']],
    ['<blockquote>q</blockquote><hr><pre>c</pre>', ['code blocks', 'dividers', 'quotes']],
    ['<p><u>u</u> <s>s</s></p>', ['strikethrough', 'underline']],
    ['<p align="center">x</p>', ['align attributes']],
    ['<body style="background:#eee"><p>x</p></body>', ['page styles']],
    ['<p><font color="red">x</font></p>', ['inline styles']],
    ['<p><marquee>x</marquee></p>', ['<marquee> elements']],
    ['<p><span style="font-weight:bold">x</span></p>', ['inline styles']],
  ])('%s', (html, expected) => {
    expect(unkeptFormatting(html)).toEqual(expected)
  })
})
