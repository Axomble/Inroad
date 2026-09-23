import { expect, test, type Locator, type Page, type Route } from '@playwright/test'

/**
 * The sequence flow canvas in a real browser.
 *
 * The unit suite drives the canvas through jsdom with React Flow's measuring
 * APIs stubbed, and fires events straight at elements — so it cannot see the
 * two things only a browser decides: whether a node's buttons actually receive
 * a pointer (React Flow sets `pointer-events: none` on the wrapper of a node
 * that is neither draggable nor selectable), and whether the edges and their
 * add buttons are drawn where a person can click them. This spec clicks them.
 *
 * Every /api/v1 route is mocked in the browser, so no API server or database is
 * needed (same approach as campaign-senders.spec.ts).
 */

const CAMPAIGN_ID = 'campaign-1'

const json = (body: unknown) => ({ status: 200, contentType: 'application/json', body: JSON.stringify(body) })

type Step = { id: string; step_order: number; delay_seconds: number; subject: string; body_text: string; body_html: string }

const TWO_STEPS: Step[] = [
  { id: 'step-1', step_order: 1, delay_seconds: 0, subject: 'A quick idea', body_text: 'hello', body_html: '' },
  { id: 'step-2', step_order: 2, delay_seconds: 3 * 86400, subject: 'Following up', body_text: 'bumping this', body_html: '' },
]

async function mockApi(
  page: Page,
  initialSteps: Step[] = TWO_STEPS,
): Promise<{ reorders: string[][]; creates: unknown[]; updates: Partial<Step>[] }> {
  const reorders: string[][] = []
  const creates: unknown[] = []
  const updates: Partial<Step>[] = []
  let steps: Step[] = initialSteps
  const membership = { workspace_id: 'workspace-e2e', workspace_name: 'Atlas Labs', role: 'owner' }

  await page.route('**/api/v1/**', async (route: Route) => {
    const request = route.request()
    const path = new URL(request.url()).pathname

    if (path.endsWith('/auth/login') || path.endsWith('/auth/refresh')) {
      return route.fulfill(
        json({
          access_token: 'browser-test-token',
          expires_in: 900,
          user_id: 'user-e2e',
          active_workspace_id: 'workspace-e2e',
          role: 'owner',
          memberships: [membership],
        }),
      )
    }
    if (path.endsWith('/auth/me')) {
      return route.fulfill(
        json({
          user_id: 'user-e2e',
          active_workspace_id: 'workspace-e2e',
          role: 'owner',
          memberships: [membership],
          email_verified: true,
        }),
      )
    }

    if (path.endsWith('/variants')) return route.fulfill(json([]))
    if (path.endsWith('/custom-fields')) {
      return route.fulfill(
        json([
          { id: 'cf-1', key: 'industry', label: 'Industry', type: 'text', options: [], created_at: '', archived: false, archived_at: null },
        ]),
      )
    }
    const stepMatch = path.match(/\/steps\/(step-\d+)$/)
    if (stepMatch && request.method() === 'PUT') {
      const body = request.postDataJSON() as Partial<Step>
      updates.push(body)
      const step = steps.find((s) => s.id === stepMatch[1])
      return route.fulfill(json({ ...step, ...body }))
    }
    if (path.endsWith(`/campaigns/${CAMPAIGN_ID}/steps/reorder`)) {
      const { step_ids } = request.postDataJSON() as { step_ids: string[] }
      reorders.push(step_ids)
      steps = step_ids.flatMap((id, index) => {
        const step = steps.find((s) => s.id === id)
        return step ? [{ ...step, step_order: index + 1 }] : []
      })
      return route.fulfill(json(steps))
    }
    if (path.endsWith(`/campaigns/${CAMPAIGN_ID}/steps`)) {
      if (request.method() === 'POST') {
        const body = request.postDataJSON() as Partial<Step>
        creates.push(body)
        const created: Step = {
          id: `step-${steps.length + 1}`,
          step_order: steps.length + 1,
          delay_seconds: body.delay_seconds ?? 0,
          subject: body.subject ?? '',
          body_text: body.body_text ?? '',
          body_html: '',
        }
        steps = [...steps, created]
        return route.fulfill(json(created))
      }
      return route.fulfill(json(steps))
    }
    if (path.endsWith(`/campaigns/${CAMPAIGN_ID}`)) {
      return route.fulfill(
        json({ id: CAMPAIGN_ID, name: 'Founder signal', subject: 'A quick idea', status: 'draft', tracking_enabled: true, stats: {}, metrics: {} }),
      )
    }
    if (path.endsWith('/campaigns')) {
      return route.fulfill(json([{ id: CAMPAIGN_ID, name: 'Founder signal', subject: 'A quick idea', status: 'draft', stats: {} }]))
    }
    if (path.endsWith('/mailboxes') || path.endsWith('/lists')) return route.fulfill(json([]))
    if (path.includes('/contacts')) {
      return route.fulfill(json({ items: [], next_cursor: null, prev_cursor: null, total: 0, total_is_capped: false }))
    }
    return route.fulfill({ status: 404, contentType: 'application/json', body: '{"error":"unhandled e2e route"}' })
  })

  return { reorders, creates, updates }
}

async function signIn(page: Page) {
  await page.goto('/')
  await page.getByLabel('Email').fill('demo@inroad.test')
  await page.getByRole('textbox', { name: 'Password', exact: true }).fill('correct-horse-battery-staple')
  await page.getByRole('button', { name: 'Log in' }).click()
  await page.waitForURL(/\/app$/)
  await expect(page.getByRole('main')).toBeVisible()
}

test('the steps tab opens on the flow, and its node and edge controls take real clicks', async ({ page }) => {
  const { reorders, creates } = await mockApi(page)
  await signIn(page)
  await page.goto(`/app/campaigns/${CAMPAIGN_ID}/steps`)

  const canvas = page.getByRole('region', { name: 'Sequence flow' })
  await expect(canvas).toBeVisible()
  await expect(canvas.getByText('Start')).toBeVisible()
  await expect(canvas.getByText('Stop')).toBeVisible()
  await expect(canvas.getByText('Wait 3 days')).toBeVisible()

  // A node's own button: moving step 1 down is a reorder.
  await canvas.getByRole('button', { name: 'Move step 1 down' }).click()
  await expect.poll(() => reorders.at(-1)).toEqual(['step-2', 'step-1'])

  // An edge's add button, then the existing step form beside the canvas.
  await canvas.getByRole('button', { name: 'Add a step after step 1' }).click()
  const panel = page.getByRole('complementary', { name: 'New step after step 1' })
  await panel.getByLabel('Subject').fill('Middle touch')
  await panel.getByRole('button', { name: 'Add step' }).click()

  await expect.poll(() => creates.length).toBe(1)
  // Created at the end, then placed after (the now-first) step-2.
  await expect.poll(() => reorders.at(-1)).toEqual(['step-2', 'step-3', 'step-1'])
  await expect(canvas.getByRole('button', { name: 'Edit step 2: Middle touch' })).toBeVisible()
  // Focus moved to the new step, not back to a "+" that no longer exists.
  await expect(canvas.getByRole('button', { name: 'Edit step 2: Middle touch' })).toBeFocused()

  // The list is still one click away.
  await page.getByRole('button', { name: 'List' }).click()
  await expect(canvas).toHaveCount(0)
  await expect(page.getByText('3 days after previous')).toBeVisible()
})

/** True when `inner`'s box sits entirely inside `outer`'s. */
async function isInside(inner: Locator, outer: Locator): Promise<boolean> {
  const [a, b] = await Promise.all([inner.boundingBox(), outer.boundingBox()])
  if (!a || !b) return false
  return a.x >= b.x && a.y >= b.y && a.x + a.width <= b.x + b.width && a.y + a.height <= b.y + b.height
}

test('a long sequence stays readable and the canvas follows keyboard focus to offscreen steps', async ({ page }) => {
  const twelve: Step[] = Array.from({ length: 12 }, (_, index) => ({
    id: `step-${index + 1}`,
    step_order: index + 1,
    delay_seconds: 86400,
    subject: `Touch ${index + 1}`,
    body_text: 'hello',
    body_html: '',
  }))
  await mockApi(page, twelve)
  await signIn(page)
  await page.goto(`/app/campaigns/${CAMPAIGN_ID}/steps`)

  const canvas = page.getByRole('region', { name: 'Sequence flow' })
  const first = canvas.getByRole('button', { name: 'Edit step 1: Touch 1' })
  const last = canvas.getByRole('button', { name: 'Edit step 12: Touch 12' })
  await expect(first).toBeVisible()
  // Shown from the top at a readable zoom, not shrunk to fit all twelve.
  await expect.poll(() => isInside(first, canvas)).toBe(true)
  expect(await isInside(last, canvas)).toBe(false)

  // Keyboard focus on the last step pans the canvas to it.
  await last.focus()
  await expect.poll(() => isInside(last, canvas)).toBe(true)
})

test('dragging from a step’s exit onto another step makes it next', async ({ page }) => {
  const three: Step[] = [
    ...TWO_STEPS,
    { id: 'step-3', step_order: 3, delay_seconds: 86400, subject: 'Last call', body_text: 'closing', body_html: '' },
  ]
  const { reorders } = await mockApi(page, three)
  await signIn(page)
  await page.goto(`/app/campaigns/${CAMPAIGN_ID}/steps`)
  const canvas = page.getByRole('region', { name: 'Sequence flow' })
  await expect(canvas.getByText('Last call')).toBeVisible()

  const from = canvas.locator('.react-flow__node[data-id="step-1"] .react-flow__handle.source')
  const to = canvas.locator('.react-flow__node[data-id="step-3"] .react-flow__handle.target')
  const [a, b] = await Promise.all([from.boundingBox(), to.boundingBox()])
  if (!a || !b) throw new Error('handles not rendered')
  await page.mouse.move(a.x + a.width / 2, a.y + a.height / 2)
  await page.mouse.down()
  await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2, { steps: 12 })
  await page.mouse.up()

  await expect.poll(() => reorders.at(-1)).toEqual(['step-1', 'step-3', 'step-2'])
  await expect(canvas.getByRole('button', { name: 'Edit step 2: Last call' })).toBeVisible()
})

// The step editor's merge fields in a real browser: jsdom can't place a caret,
// type through the browser's own input pipeline, or make a node view
// non-editable, so the unit suite drives the editor through its API instead.
test('the step editor turns merge fields into chips, flags an unknown one, and refuses to save it', async ({ page }) => {
  const { updates } = await mockApi(page)
  await signIn(page)
  await page.goto(`/app/campaigns/${CAMPAIGN_ID}/steps`)

  const canvas = page.getByRole('region', { name: 'Sequence flow' })
  await canvas.getByRole('button', { name: 'Edit step 1: A quick idea' }).click()
  const panel = page.getByRole('complementary', { name: 'Step 1' })
  const body = panel.getByRole('textbox', { name: 'Body' })
  await expect(body).toHaveText('hello')

  // `{{` opens the menu; the workspace's custom field is offered by label.
  await body.click()
  await page.keyboard.press('End')
  await page.keyboard.type(' {{ind')
  const menu = panel.getByRole('listbox', { name: 'Merge fields' })
  await expect(menu.getByRole('option')).toHaveText(['Industry{{custom.industry}}'])
  await page.keyboard.press('Enter')
  await expect(menu).toHaveCount(0)
  const chip = body.locator('[data-variable-status="known"]')
  await expect(chip).toHaveText('{{custom.industry}}')
  // An atom: the browser can't put a caret inside it.
  await expect(body.locator('.node-variable')).toHaveAttribute('contenteditable', 'false')

  // A typo typed by hand becomes a flagged chip, and Save does nothing.
  await page.keyboard.type(' {{firstname}}')
  await expect(body.locator('[data-variable-status="unknown"]')).toHaveText('{{firstname}}?')
  await expect(panel.getByRole('alert')).toContainText('{{firstname}}')
  await panel.getByRole('button', { name: 'Save step' }).click()
  await page.waitForTimeout(300)
  expect(updates).toHaveLength(0)

  // One Backspace removes the whole chip; then bold everything and save.
  // focus(), not click(): ProseMirror restores its own caret (just after the
  // chip), where a click at the box's centre could land on a chip and select it.
  await body.focus()
  await page.keyboard.press('Backspace')
  await expect(panel.getByRole('alert')).toHaveCount(0)
  await page.keyboard.press('ControlOrMeta+a')
  await panel.getByRole('button', { name: 'Bold' }).click()
  await panel.getByRole('button', { name: 'Save step' }).click()

  await expect.poll(() => updates.length).toBe(1)
  expect(updates[0]).toMatchObject({
    subject: 'A quick idea',
    body_text: 'hello {{custom.industry}} ',
    body_html: '<p><strong>hello {{custom.industry}} </strong></p>',
  })
})

// Stored HTML the editor's schema can't hold (a table from an API client) must
// never be rewritten by opening and saving the step.
test('a step whose HTML the editor can’t keep opens as HTML and an untouched save sends it back byte for byte', async ({ page }) => {
  const tableHtml =
    '<h2 style="margin:0">Plans for {{company}}</h2>\n<table style="border-collapse:collapse"><tr><td>Starter</td></tr></table>'
  const { updates } = await mockApi(page, [
    { id: 'step-1', step_order: 1, delay_seconds: 0, subject: 'A quick idea', body_text: 'Plans: Starter', body_html: tableHtml },
  ])
  await signIn(page)
  await page.goto(`/app/campaigns/${CAMPAIGN_ID}/steps`)

  await page.getByRole('region', { name: 'Sequence flow' }).getByRole('button', { name: 'Edit step 1: A quick idea' }).click()
  const panel = page.getByRole('complementary', { name: 'Step 1' })
  await expect(panel.getByRole('status')).toContainText('can’t keep: headings, inline styles and tables')
  await expect(panel.getByRole('textbox', { name: 'Body HTML' })).toHaveValue(tableHtml)
  await expect(panel.locator('[contenteditable="true"]')).toHaveCount(1) // the subject only

  await panel.getByRole('button', { name: 'Save step' }).click()
  await expect.poll(() => updates.length).toBe(1)
  expect(updates[0]).toMatchObject({ body_html: tableHtml, body_text: 'Plans: Starter' })
})
