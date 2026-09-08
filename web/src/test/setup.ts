// The `/vitest` entrypoint, not the bare package: the bare one re-exports
// jest.d.ts, which augments JEST's `Assertion` interface. Vitest's matchers
// typecheck against that only by coincidence — v5 makes `Assertion` generic in
// two parameters, so the jest augmentation stops applying and every
// `toBeInTheDocument` / `toHaveAttribute` call becomes TS2339 (~1400 of them).
// This entrypoint augments `declare module 'vitest'` directly, which is the
// module we actually assert through.
import '@testing-library/jest-dom/vitest'

// The base query in `store/empty-api.ts` reads `VITE_API_BASE_URL` and falls
// back to the bare path `/api/v1` for the same-origin production case. Under
// vitest, the fetch implementation is Node's undici — which cannot resolve a
// bare path against `document.location` — so we set an absolute base for the
// test environment. `document.location` is `http://localhost:5173/` per
// vitest.config.ts's jsdom URL setting.
import.meta.env.VITE_API_BASE_URL = 'http://localhost:5173/api/v1'

// jsdom exposes canvas.getContext but logs a "not implemented" error on every
// call unless the optional native canvas package is installed. Rendering tests
// do not assert pixels; returning null exercises the component's supported
// no-canvas fallback and keeps test output actionable.
Object.defineProperty(HTMLCanvasElement.prototype, 'getContext', {
  configurable: true,
  value: () => null,
})
