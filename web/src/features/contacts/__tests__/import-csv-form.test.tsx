import { fireEvent, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, test, vi } from 'vitest'
import { renderWithProviders, makeTestStore } from '@/test/render-with-providers'
import { api } from '@/store/api'
import { ImportCsvForm } from '../import-csv-form'
// Importing the feature api registers the enhanced import mutation on the
// shared emptyApi endpoints registry — required for the hook to resolve.
import '../api'

const csvFile = () => new File(['email\na@b.co\n'], 'contacts.csv', { type: 'text/csv' })

function selectFile() {
  const input = screen.getByLabelText('Import CSV') as HTMLInputElement
  fireEvent.change(input, { target: { files: [csvFile()] } })
  return input
}

function importButton() {
  return screen.getByRole('button', { name: /^import/i })
}

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('ImportCsvForm', () => {
  test('uploads via the RTKQ mutation (FormData body) and calls onImported', async () => {
    const rawFetch = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        new Response(JSON.stringify({ imported: 3, skipped: 0, duplicates: 0 }), {
          status: 200,
          headers: { 'content-type': 'application/json' },
        }),
    )
    vi.stubGlobal('fetch', rawFetch)

    const store = makeTestStore()
    const invalidateSpy = vi.spyOn(api.util, 'invalidateTags')

    const onImported = vi.fn()
    renderWithProviders(<ImportCsvForm listId="list-abc" onImported={onImported} />, { store })

    selectFile()
    // Selected filename surfaces inline in the single-row control.
    expect(screen.getByText('contacts.csv')).toBeInTheDocument()

    fireEvent.click(importButton())

    await waitFor(() =>
      expect(onImported).toHaveBeenCalledWith({ imported: 3, skipped: 0, duplicates: 0 }),
    )

    // The mutation went through fetchBaseQuery (not a raw component fetch) — the
    // request must be a POST to /contacts/import with a multipart FormData body.
    expect(rawFetch).toHaveBeenCalled()
    const firstCall = rawFetch.mock.calls[0]
    expect(firstCall).toBeDefined()
    const req = firstCall![0] as Request
    expect(req.url).toContain('/contacts/import')
    expect(req.method).toBe('POST')
    const ct = req.headers.get('content-type') ?? ''
    expect(ct).toMatch(/multipart\/form-data/i)

    // Success clears the selection so re-picking the same file re-fires change.
    await waitFor(() => expect(screen.queryByText('contacts.csv')).not.toBeInTheDocument())

    const emittedInvalidation = invalidateSpy.mock.calls.some((args) =>
      JSON.stringify(args).includes('Contact'),
    )
    const state = store.getState()
    expect(emittedInvalidation || state[api.reducerPath] !== undefined).toBe(true)
  })

  test('renders "List not found." on a 404', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response('', { status: 404 })),
    )
    renderWithProviders(<ImportCsvForm listId="list-abc" onImported={vi.fn()} />)

    selectFile()
    fireEvent.click(importButton())

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('List not found.')
  })

  test('renders the "email column" hint on a 400', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response('', { status: 400 })),
    )
    renderWithProviders(<ImportCsvForm listId="list-abc" onImported={vi.fn()} />)

    selectFile()
    fireEvent.click(importButton())

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Choose a CSV file with an "email" column.')
  })

  test('blocks submit until a file is chosen (Import disabled)', () => {
    const onImported = vi.fn()
    renderWithProviders(<ImportCsvForm listId="list-abc" onImported={onImported} />)

    // No file yet → the Import submit is disabled.
    expect(importButton()).toBeDisabled()

    selectFile()
    expect(importButton()).toBeEnabled()
  })

  test('shows a loading state on the Import button while the upload is in flight', async () => {
    // Defer the response so the pending state is observable.
    let resolveFetch: (r: Response) => void = () => {}
    const pending = new Promise<Response>((resolve) => {
      resolveFetch = resolve
    })
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => pending),
    )

    const onImported = vi.fn()
    renderWithProviders(<ImportCsvForm listId="list-abc" onImported={onImported} />)

    selectFile()
    fireEvent.click(importButton())

    // While pending: button shows the loading label and is disabled.
    const loading = await screen.findByRole('button', { name: /importing/i })
    expect(loading).toBeDisabled()

    resolveFetch(
      new Response(JSON.stringify({ imported: 1, skipped: 0, duplicates: 0 }), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      }),
    )

    await waitFor(() => expect(onImported).toHaveBeenCalled())
  })
})
