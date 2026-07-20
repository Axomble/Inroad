import '@testing-library/jest-dom'

// The base query in `store/empty-api.ts` reads `VITE_API_BASE_URL` and falls
// back to the bare path `/api/v1` for the same-origin production case. Under
// vitest, the fetch implementation is Node's undici — which cannot resolve a
// bare path against `document.location` — so we set an absolute base for the
// test environment. `document.location` is `http://localhost:5173/` per
// vitest.config.ts's jsdom URL setting.
import.meta.env.VITE_API_BASE_URL = 'http://localhost:5173/api/v1'
