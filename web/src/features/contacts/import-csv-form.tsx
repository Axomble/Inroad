import { useId, useRef, useState } from 'react'
import { Loader2, Upload } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import type { ImportResult } from '@/store/api'
import { useImportContactsCsvMutation } from './api'

/**
 * CSV import control.
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
  const inputId = useId()
  const inputRef = useRef<HTMLInputElement>(null)
  const [importCsv, { isLoading }] = useImportContactsCsvMutation()

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!file) return
    setError(null)
    const result = await importCsv({ list: listId, file })
    if ('error' in result && result.error) {
      const status = (result.error as { status?: number }).status
      setError(importErrorMessage(status))
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
    <form onSubmit={onSubmit} className="flex flex-wrap items-end gap-3">
      <div>
        <Label htmlFor={inputId}>Import CSV</Label>
        <Input
          id={inputId}
          ref={inputRef}
          type="file"
          accept=".csv,text/csv"
          className="mt-1.5"
          onChange={(e) => setFile(e.target.files?.[0] ?? null)}
        />
      </div>
      <Button type="submit" variant="primary" size="sm" disabled={!file || isLoading}>
        {isLoading ? <Loader2 className="animate-spin" /> : <Upload className="size-4" />}
        {isLoading ? 'Importing…' : 'Import'}
      </Button>
      {error && (
        <p role="alert" className="w-full text-xs text-danger">
          {error}
        </p>
      )}
    </form>
  )
}
