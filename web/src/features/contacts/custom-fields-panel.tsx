import { useState } from 'react'
import { Archive, Loader2, Plus } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Skeleton } from '@/components/ui/skeleton'
import { NoticeBanner, type Notice } from '@/components/shared/notice-banner'
import {
  AlertDialog,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog'
import { Page, PageTopbar, PageBody, SectionBar, EmptyBlock } from '@/components/layout/page'
import { useListCustomFieldsQuery, useArchiveCustomFieldMutation, type CustomFieldDef } from './api'
import { CustomFieldDialog } from './custom-field-dialog'
import { customFieldErrorMessage } from './custom-field-error-messages'

const TYPE_LABEL: Record<CustomFieldDef['type'], string> = {
  text: 'Text',
  number: 'Number',
  date: 'Date',
  select: 'Select',
}

/**
 * Settings → Custom fields. What a contact can hold beyond the built-in columns,
 * which CSV headers an import will recognise, and which `{{custom.*}}` tokens a
 * sequence may use.
 *
 * Archived fields are listed in their own section rather than hidden. They are
 * the explanation for values a contact still shows under a key no form offers,
 * and a settings page that hid them would make that data look like corruption.
 */
export function CustomFieldsPanel() {
  const { data, isLoading, isError, refetch } = useListCustomFieldsQuery()
  const [notice, setNotice] = useState<Notice | null>(null)
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<CustomFieldDef | null>(null)
  const [archiving, setArchiving] = useState<CustomFieldDef | null>(null)

  const fields = data ?? []
  const live = fields.filter((f) => !f.archived)
  const archived = fields.filter((f) => f.archived)

  return (
    <Page>
      <PageTopbar
        eyebrow="Workspace"
        title="Custom fields"
        subtitle="Extra contact data you can import, edit, and personalize sequences with"
        actions={
          <Button variant="primary" size="sm" disabled={isLoading || isError} onClick={() => setCreating(true)}>
            <Plus className="size-4" />
            New field
          </Button>
        }
      />

      <PageBody>
        {notice && <NoticeBanner notice={notice} />}

        {isLoading ? (
          <LoadingRows />
        ) : isError ? (
          <EmptyBlock
            title="Couldn't load custom fields"
            description="Something went wrong fetching this workspace's custom fields. Please try again."
            action={
              <Button variant="outline" size="sm" onClick={() => void refetch()}>
                Retry
              </Button>
            }
          />
        ) : fields.length === 0 ? (
          <EmptyBlock
            title="No custom fields"
            description="Define a field to store data like industry or renewal date on each contact, map it from a CSV column, and personalize sequences with it."
            action={
              <Button variant="primary" size="sm" onClick={() => setCreating(true)}>
                <Plus className="size-4" />
                Create your first field
              </Button>
            }
          />
        ) : (
          <>
            <SectionBar label="Fields" count={live.length} />
            <div>
              {live.map((field) => (
                <FieldRow
                  key={field.id}
                  field={field}
                  onEdit={() => setEditing(field)}
                  onArchive={() => setArchiving(field)}
                />
              ))}
            </div>

            {archived.length > 0 && (
              <>
                <SectionBar label="Archived" count={archived.length} />
                <p className="px-5 pb-2 text-xs text-muted-foreground">
                  These no longer appear on contacts or in imports, and a sequence token naming one will fail
                  preflight. Values contacts already hold under them are untouched and still send.
                </p>
                <div>
                  {archived.map((field) => (
                    <FieldRow key={field.id} field={field} />
                  ))}
                </div>
              </>
            )}
          </>
        )}
      </PageBody>

      {creating && <CustomFieldDialog onClose={() => setCreating(false)} onNotice={setNotice} />}
      {editing && (
        <CustomFieldDialog initial={editing} onClose={() => setEditing(null)} onNotice={setNotice} />
      )}
      {archiving && (
        <ArchiveFieldDialog field={archiving} onClose={() => setArchiving(null)} onNotice={setNotice} />
      )}
    </Page>
  )
}

/** One definition. Actions are omitted entirely for an archived row — there is
 * nothing to do to it, and a disabled button would only invite the click. */
function FieldRow({
  field,
  onEdit,
  onArchive,
}: {
  field: CustomFieldDef
  onEdit?: () => void
  onArchive?: () => void
}) {
  return (
    <div className="flex items-center gap-3 border-b border-border px-5 py-3">
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate text-sm font-medium">{field.label}</span>
          <Badge variant="outline">{TYPE_LABEL[field.type]}</Badge>
        </div>
        <p className="truncate text-xs text-muted-foreground">
          <code className="font-mono">{`{{custom.${field.key}}}`}</code>
          {field.type === 'select' && field.options.length > 0 && ` · ${field.options.join(', ')}`}
        </p>
      </div>
      {onEdit && (
        <Button variant="outline" size="sm" onClick={onEdit}>
          Edit
        </Button>
      )}
      {onArchive && (
        <Button variant="ghost" size="sm" onClick={onArchive}>
          <Archive className="size-3.5" />
          Archive
        </Button>
      )}
    </div>
  )
}

/**
 * Archiving is spelled out rather than confirmed with a generic "are you sure",
 * because what it does NOT do is the surprising part: values survive, and the
 * key is burned permanently.
 */
function ArchiveFieldDialog({
  field,
  onClose,
  onNotice,
}: {
  field: CustomFieldDef
  onClose: () => void
  onNotice: (n: Notice) => void
}) {
  const [archiveField, { isLoading }] = useArchiveCustomFieldMutation()

  async function onConfirm() {
    const result = await archiveField({ fieldId: field.id })
    onClose()
    if ('error' in result) {
      onNotice({ tone: 'error', text: customFieldErrorMessage('archive', result.error) })
    } else {
      onNotice({ tone: 'ok', text: `Field “${field.label}” was archived.` })
    }
  }

  return (
    <AlertDialog open onOpenChange={(next) => !next && !isLoading && onClose()}>
      <AlertDialogContent>
        <AlertDialogHeader>
          <AlertDialogTitle>Archive “{field.label}”?</AlertDialogTitle>
          <AlertDialogDescription>
            Contacts keep the values they already hold, and those values keep sending. What stops is new data: the
            field disappears from contact forms and CSV import, and any sequence using{' '}
            <code className="font-mono">{`{{custom.${field.key}}}`}</code> will fail preflight until you fix it.
            <br />
            <br />
            The key <code className="font-mono">{field.key}</code> stays reserved and cannot be reused — a new field
            with the same key would inherit this one’s values.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <AlertDialogFooter>
          <Button variant="ghost" size="sm" onClick={onClose} disabled={isLoading}>
            Cancel
          </Button>
          <Button variant="destructive" size="sm" disabled={isLoading} onClick={() => void onConfirm()}>
            {isLoading && <Loader2 className="size-3.5 animate-spin" />}
            Archive field
          </Button>
        </AlertDialogFooter>
      </AlertDialogContent>
    </AlertDialog>
  )
}

function LoadingRows() {
  return (
    <div>
      {[0, 1, 2].map((i) => (
        <div key={i} className="flex items-center gap-3 border-b border-border px-5 py-3">
          <div className="flex-1 space-y-2">
            <Skeleton className="h-3.5 w-40" />
            <Skeleton className="h-2.5 w-56" />
          </div>
          <Skeleton className="h-8 w-16" />
          <Skeleton className="h-8 w-20" />
        </div>
      ))}
    </div>
  )
}
