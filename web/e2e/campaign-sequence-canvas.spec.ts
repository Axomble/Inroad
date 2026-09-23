import { expect, test, type Page, type Route } from '@playwright/test'

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

async function mockApi(page: Page): Promise<{ reorders: string[][]; creates: unknown[] }> {
  const reorders: string[][] = []
  const creates: unknown[] = []
  let steps: Step[] = [
    { id: 'step-1', step_order: 1, delay_seconds: 0, subject: 'A quick idea', body_text: 'hello', body_html: '' },
    { id: 'step-2', step_order: 2, delay_seconds: 3 * 86400, subject: '', body_text: 'bumping this', body_html: '' },
  ]
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

  return { reorders, creates }
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

  // The list is still one click away.
  await page.getByRole('button', { name: 'List' }).click()
  await expect(canvas).toHaveCount(0)
  await expect(page.getByText('3 days after previous')).toBeVisible()
})
