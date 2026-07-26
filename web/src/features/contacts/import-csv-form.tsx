import { useRef, useState } from 'react'
import { Loader2, Upload } from 'lucide-react'
import { Button } from '@/components/ui/button'
import type { ImportResult } from '@/store/api'
import { httpStatus } from '@/lib/rtk-error'
import { useImportContactsCsvMutation } from './api'

/**
 * CSV import control — a compact, single-row picker sized to live inside a
 * `SectionBar` (a fixed 40px `h-10` row). The native file input is visually
 * hidden (`sr-only`, but still keyboard/screen-reader reachable via its
 * `aria-label`) and driven by a small "Choose CSV" button; the selected
 * filename shows inline (truncated) and the "Import" submit sits beside it.
 * Any error surfaces just BELOW the bar (absolutely positioned) so it stays
 * fully readable without ever growing the single-row bar.
 *
 * The generated `useImportContactsMutation` in store/api.ts types its body as
 * `{ file?: Blob }`, but RTK Query's fetchBaseQuery treats plain objects as
 * JSON: it would `JSON.stringify` the File to `"{}"` and the bytes would never
 * leave the browser. Our feature's `api.ts` overrides that with an
 * `importContactsCsv` mutation that builds a real `FormData` body — which
 * fetchBaseQuery passes through untouched — so the file actually uploads.
 * Going through RTKQ (rather than a raw `fetch`) keeps reauth-on-401 in play
 * and lets the mutation invalidate the Contact list tag on success.
 */
function importErrorMessage(status?: number): string {
  if (status === 404) return 'List not found.'
  if (status === 400) return 'Choose a CSV file with an "email" column.'
  return "Couldn't import contacts. Please try again."
}

export function ImportCsvForm({
  listId,
  onImported,
}: {
  listId: string
  onImported: (result: ImportResult) => void
}) {
  const [file, setFile] = useState<File | null>(null)
  const [error, setError] = useState<string | null>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const [importCsv, { isLoading }] = useImportContactsCsvMutation()

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!file) return
    setError(null)
    const result = await importCsv({ list: listId, file })
    if ('error' in result && result.error) {
      setError(importErrorMessage(httpStatus(result.error)))
      return
    }
    if ('data' in result && result.data) {
      onImported(result.data)
      // Clear both the DOM value AND local state, so re-selecting the same
      // file re-fires the input's change event.
      if (inputRef.current) inputRef.current.value = ''
      setFile(null)
    }
  }

  return (
    <form onSubmit={(e) => void onSubmit(e)} className="relative flex items-center gap-2">
      {/* Visually hidden but reachable: the accessible name lives here so the
          "Choose CSV" button can stay icon+label without a stacked <Label>. */}
      <input
        ref={inputRef}
        type="file"
        accept=".csv,text/csv"
        aria-label="Import CSV"
        className="sr-only"
        onChange={(e) => {
          setError(null)
          setFile(e.target.files?.[0] ?? null)
        }}
      />
      <Button type="button" variant="secondary" size="xs" onClick={() => inputRef.current?.click()}>
        <Upload className="size-3.5" />
        Choose CSV
      </Button>
      {file && (
        <span className="max-w-[9rem] truncate text-xs text-muted-foreground" title={file.name}>
          {file.name}
        </span>
      )}
      <Button type="submit" variant="primary" size="xs" disabled={!file || isLoading}>
        {isLoading ? <Loader2 className="size-3.5 animate-spin" /> : <Upload className="size-3.5" />}
        {isLoading ? 'Importing…' : 'Import'}
      </Button>
      {error && (
        <p
          role="alert"
          className="absolute right-0 top-full z-10 mt-1 whitespace-nowrap rounded-md border border-border bg-surface px-2 py-1 text-xs text-danger shadow-sm"
        >
          {error}
        </p>
      )}
    </form>
  )
}
