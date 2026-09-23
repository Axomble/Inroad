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
): Promise<{ reorders: string[][]; creates: unknown[]; branchWrites: unknown[] }> {
  const reorders: string[][] = []
  const creates: unknown[] = []
  const branchWrites: unknown[] = []
  let steps: Step[] = initialSteps
  // Branches by source step, as the server stores them: one per step.
  const branches = new Map<string, Record<string, unknown>>()
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
    if (path.endsWith('/reply-labels')) {
      const label = (key: string, name: string, stops: boolean) => ({
        id: `rl-${key}`,
        key,
        label: name,
        color: '#888888',
        position: 0,
        is_builtin: true,
        stops_enrollment: stops,
        is_automated: false,
        suppresses_contact: false,
        captures_deal: false,
        defers_enrollment: false,
        created_at: '2026-09-01T00:00:00Z',
        updated_at: '2026-09-01T00:00:00Z',
      })
      return route.fulfill(json({ labels: [label('interested', 'Interested', true), label('question', 'Question', false)] }))
    }
    if (path.endsWith(`/campaigns/${CAMPAIGN_ID}/graph`)) {
      return route.fulfill(
        json({
          campaign_id: CAMPAIGN_ID,
          entry_step_id: steps[0]?.id ?? null,
          nodes: steps.map((step, index) => ({
            step_id: step.id,
            step_order: step.step_order,
            default_next_step_id: steps[index + 1]?.id ?? null,
            branch: branches.get(step.id) ?? null,
          })),
        }),
      )
    }
    const branchPath = /\/steps\/([^/]+)\/branch$/.exec(path)
    if (branchPath?.[1]) {
      const stepId = branchPath[1]
      if (request.method() === 'DELETE') {
        branches.delete(stepId)
        return route.fulfill({ status: 204, body: '' })
      }
      const body = request.postDataJSON() as Record<string, unknown>
      branchWrites.push(body)
      const saved = { step_id: stepId, updated_at: '2026-09-23T00:00:00Z', ...body }
      branches.set(stepId, saved)
      return route.fulfill(json(saved))
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

  return { reorders, creates, branchWrites }
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

test('adding a condition draws the IF after its step, with Yes / No exits routed where they were set', async ({ page }) => {
  const three: Step[] = [
    { ...TWO_STEPS[0]!, body_html: '<p>hello</p>' },
    TWO_STEPS[1]!,
    { id: 'step-3', step_order: 3, delay_seconds: 86400, subject: 'Last call', body_text: 'closing', body_html: '' },
  ]
  const { branchWrites } = await mockApi(page, three)
  await signIn(page)
  await page.goto(`/app/campaigns/${CAMPAIGN_ID}/steps`)
  const canvas = page.getByRole('region', { name: 'Sequence flow' })
  await expect(canvas.getByText('Last call')).toBeVisible()

  await canvas.getByRole('button', { name: 'Add a condition after step 1' }).click()
  const panel = page.getByRole('complementary', { name: 'Condition after step 1' })
  await panel.getByLabel('If they…').selectOption('opened')
  await panel.getByLabel('Within (days)').fill('3')
  await panel.getByLabel('Yes — go to').selectOption('step-3')
  await panel.getByLabel('No — go to').selectOption('step-2')
  await panel.getByRole('button', { name: 'Add condition' }).click()

  await expect.poll(() => branchWrites.at(-1)).toEqual({
    condition: 'opened',
    within_days: 3,
    reply_label_key: null,
    yes_step_id: 'step-3',
    no_step_id: 'step-2',
  })

  // The diamond, in words, right after step 1 — and it took focus.
  const condition = canvas.getByRole('button', { name: 'Edit the condition after step 1: Opened within 3 days' })
  await expect(condition).toBeVisible()
  await expect(condition).toBeFocused()
  await expect(canvas.locator('.react-flow__node[data-id="cond:step-1"] polygon')).toHaveCount(1)
  // Labelled exits, each an edge to the step it was set to.
  const node = canvas.locator('.react-flow__node[data-id="cond:step-1"]')
  await expect(node.getByText('Yes', { exact: true })).toBeVisible()
  await expect(node.getByText('No', { exact: true })).toBeVisible()
  await expect(canvas.getByTestId('rf__edge-cond:step-1:yes->step-3')).toHaveCount(1)
  await expect(canvas.getByTestId('rf__edge-cond:step-1:no->step-2')).toHaveCount(1)

  // Dragging the Yes exit onto step 2 re-routes it — a real pointer drag.
  const from = node.locator('.react-flow__handle.source').first()
  const to = canvas.locator('.react-flow__node[data-id="step-2"] .react-flow__handle.target')
  const [a, b] = await Promise.all([from.boundingBox(), to.boundingBox()])
  if (!a || !b) throw new Error('handles not rendered')
  await page.mouse.move(a.x + a.width / 2, a.y + a.height / 2)
  await page.mouse.down()
  await page.mouse.move(b.x + b.width / 2, b.y + b.height / 2, { steps: 12 })
  await page.mouse.up()
  await expect.poll(() => branchWrites.at(-1)).toMatchObject({ yes_step_id: 'step-2', no_step_id: 'step-2' })
})
