import { useId, useState } from 'react'
import { Loader2, Upload } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { useAppSelector } from '@/store/hooks'
import type { ImportResult } from '@/store/api'

/**
 * CSV import control.
 *
 * The generated `useImportContactsMutation` hook (store/api.ts) types its arg
 * as `{ list: string, body: { file?: Blob } }`, but RTK Query's `fetchBaseQuery`
 * treats any plain object body as JSON: it runs `isJsonifiable(body)` (true for
 * `{ file }`, a plain object) and then `JSON.stringify`s it, which serializes a
 * File/Blob to `"{}"` — the bytes never leave the browser. There is no way to
 * make the generated hook send `multipart/form-data` without hand-editing the
 * generated client, which is off-limits. So this posts a real `FormData` body
 * directly with `fetch`, reusing the same bearer-token-from-the-auth-slice +
 * same-origin-`/api/v1` conventions as `store/empty-api.ts`. The backend route
 * (`contacts.Routes`) is bearer-authenticated only (no CSRF double-submit is
 * required off of the cookie, unlike `/auth/refresh`), so no CSRF header is
 * needed here.
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
  const accessToken = useAppSelector((s) => s.auth.accessToken)
  const [file, setFile] = useState<File | null>(null)
  const [isLoading, setIsLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const inputId = useId()

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!file) return
    setIsLoading(true)
    setError(null)
    try {
      const formData = new FormData()
      formData.append('file', file)
      const res = await fetch(
        `${window.location.origin}/api/v1/contacts/import?list=${encodeURIComponent(listId)}`,
        {
          method: 'POST',
          credentials: 'include',
          headers: accessToken ? { authorization: `Bearer ${accessToken}` } : undefined,
          body: formData,
        },
      )
      if (!res.ok) {
        setError(importErrorMessage(res.status))
        return
      }
      const result = (await res.json()) as ImportResult
      setFile(null)
      onImported(result)
    } catch {
      setError("Couldn't reach the server. Please try again.")
    } finally {
      setIsLoading(false)
    }
  }

  return (
    <form onSubmit={onSubmit} className="flex flex-wrap items-end gap-3">
      <div>
        <Label htmlFor={inputId}>Import CSV</Label>
        <Input
          id={inputId}
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
