import { expect, test, type Page } from '@playwright/test'

/**
 * The assistant panel's resize edge, in a real browser.
 *
 * Both faults this guards against are invisible to jsdom, which has no layout
 * and no hit-testing, so the unit suite in `features/agent/panel.test.tsx`
 * cannot reach them:
 *
 *  1. A `transition` on the panel's width re-eased on every pointer frame, so
 *     the edge trailed the cursor by ~60px and kept animating after release.
 *  2. The 8px grab strip overlaps the panel's own content by 4px. When a
 *     sibling inside the panel holds a transform it paints as a stacking
 *     context, and without an explicit z-index on the strip the two tie —
 *     DOM order then hands the inner half of the grab area to the content, so a
 *     drag begun there never reaches the handle at all.
 *
 * Every /api/v1 route is mocked in the browser, so no API server or database is
 * needed (same approach as app-shell.spec.ts).
 */

const json = (body: unknown) => ({
  status: 200,
  contentType: 'application/json',
  body: JSON.stringify(body),
})

async function mockApi(page: Page) {
  await page.route('**/api/v1/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    if (path.endsWith('/auth/login') || path.endsWith('/auth/refresh')) {
      return route.fulfill(json({
        access_token: 'browser-test-token',
        expires_in: 900,
        user_id: 'user-e2e',
        active_workspace_id: 'workspace-e2e',
        role: 'owner',
        memberships: [{ workspace_id: 'workspace-e2e', workspace_name: 'Atlas Labs', role: 'owner' }],
      }))
    }
    if (path.endsWith('/auth/me')) {
      return route.fulfill(json({
        user_id: 'user-e2e',
        active_workspace_id: 'workspace-e2e',
        role: 'owner',
        memberships: [{ workspace_id: 'workspace-e2e', workspace_name: 'Atlas Labs', role: 'owner' }],
        email_verified: true,
      }))
    }
    // The composer reads the model list; an empty-but-successful list keeps it
    // on the "no provider configured" path instead of an error path.
    if (path.endsWith('/ai/models')) return route.fulfill(json({ models: [] }))
    // The panel under test needs nothing else. The overview behind it renders
    // its own empty/error states, which this spec does not assert on.
    return route.fulfill({ status: 404, contentType: 'application/json', body: '{"error":"unhandled e2e route"}' })
  })
}

/**
 * How far the grab strip's centre may sit from the panel's left edge.
 *
 * `left: -4px` on a 8px strip would centre it exactly, but `left` is resolved
 * against the containing block's *padding* box while `getBoundingClientRect`
 * reports the border box — and the panel carries a 1px left border. So the
 * strip straddles the edge 3px outside / 5px inside. The tolerance absorbs that
 * without weakening the check: the regression this guards against (losing the
 * containing block) throws the strip hundreds of pixels away, to x≈-4.
 */
const edgeTolerance = 2

/** Geometry of the panel and its grab strip, as the browser lays them out. */
async function geometry(page: Page) {
  return page.evaluate(() => {
    const aside = document.querySelector('aside[aria-label="Inroad assistant"]')
    const handle = document.querySelector('[aria-label="Resize assistant panel"]')
    if (!aside || !handle) throw new Error('assistant panel not rendered')
    const a = aside.getBoundingClientRect()
    const h = handle.getBoundingClientRect()
    return {
      panelLeft: a.x,
      panelWidth: a.width,
      handleLeft: h.x,
      handleWidth: h.width,
    }
  })
}

async function openAssistant(page: Page) {
  await page.getByRole('button', { name: 'Open Inroad assistant' }).click()
  await expect(page.getByRole('complementary', { name: 'Inroad assistant' })).toBeVisible()
}

test.beforeEach(async ({ page }) => {
  await mockApi(page)
  await page.goto('/')
  await page.getByLabel('Email').fill('demo@inroad.test')
  await page.getByRole('textbox', { name: 'Password', exact: true }).fill('correct-horse-battery-staple')
  await page.getByRole('button', { name: 'Log in' }).click()
  await expect(page.getByRole('heading', { name: 'Your outreach command center.' })).toBeVisible()
})

test('the whole grab strip is reachable and centred on the panel edge', async ({ page }) => {
  await openAssistant(page)
  const box = await geometry(page)

  // The strip straddles the panel's edge, part over the workspace and part over
  // the panel. If the panel ever stops being a containing block for it, the
  // strip lands at the viewport's left edge instead and this fails loudly.
  expect(Math.abs(box.handleLeft + box.handleWidth / 2 - box.panelLeft))
    .toBeLessThanOrEqual(edgeTolerance)

  // Every point across the strip must hit the strip itself — including the
  // inner half, which overlaps the panel's own animated content.
  const hits = await page.evaluate(({ left, width }) => {
    const samples: string[] = []
    for (const fraction of [0.1, 0.3, 0.5, 0.7, 0.9]) {
      const el = document.elementFromPoint(left + width * fraction, 400)
      samples.push(el?.getAttribute('aria-label') ?? el?.tagName ?? 'null')
    }
    return samples
  }, { left: box.handleLeft, width: box.handleWidth })

  expect(hits).toEqual(Array(5).fill('Resize assistant panel'))
})

test('the panel edge lands on the cursor with no lag, and keeps the width on release', async ({ page }) => {
  await openAssistant(page)
  const start = await geometry(page)
  const y = 400

  await page.mouse.move(start.panelLeft, y)
  await page.mouse.down()

  // Widen in steps, asserting after each move that the panel is exactly where
  // the cursor put it. A width transition shows up here as a shortfall.
  for (const delta of [40, 80, 140]) {
    // oxlint-disable-next-line no-await-in-loop -- a drag is one serial gesture; the moves cannot overlap
    await page.mouse.move(start.panelLeft - delta, y)
    // oxlint-disable-next-line no-await-in-loop -- each assertion must read the layout this move produced
    const during = await geometry(page)
    expect(during.panelWidth).toBeCloseTo(start.panelWidth + delta, 0)
    // The strip stays under the cursor for the whole drag.
    expect(Math.abs(during.handleLeft + during.handleWidth / 2 - (start.panelLeft - delta)))
      .toBeLessThanOrEqual(edgeTolerance)
  }

  await page.mouse.up()
  const released = await geometry(page)
  expect(released.panelWidth).toBeCloseTo(start.panelWidth + 140, 0)

  // Dragging inward shrinks it again, from wherever the previous drag ended.
  await page.mouse.move(released.panelLeft, y)
  await page.mouse.down()
  await page.mouse.move(released.panelLeft + 60, y)
  await page.mouse.up()
  const shrunk = await geometry(page)
  expect(shrunk.panelWidth).toBeCloseTo(released.panelWidth - 60, 0)
})

test('the drag is clamped to the panel’s size range', async ({ page }) => {
  await openAssistant(page)
  const handle = page.getByRole('separator', { name: 'Resize assistant panel' })
  const min = Number(await handle.getAttribute('aria-valuemin'))
  const max = Number(await handle.getAttribute('aria-valuemax'))
  const start = await geometry(page)
  const y = 400

  // Far past the maximum.
  await page.mouse.move(start.panelLeft, y)
  await page.mouse.down()
  await page.mouse.move(20, y)
  expect((await geometry(page)).panelWidth).toBeCloseTo(max, 0)

  // ...and far past the minimum, without releasing in between.
  await page.mouse.move(page.viewportSize()!.width - 20, y)
  expect((await geometry(page)).panelWidth).toBeCloseTo(min, 0)
  await page.mouse.up()

  expect(await handle.getAttribute('aria-valuenow')).toBe(String(min))
})
