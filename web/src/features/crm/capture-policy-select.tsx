import { Select } from '@/components/ui/select'
import { useCrmGetSettingsQuery, useCrmUpdateSettingsMutation, type AutoCapturePolicy } from './api'
import { crmErrorMessage } from './error-copy'

/**
 * How much of the reply stream is allowed to open a deal on its own. It sits
 * beside the deals it creates rather than under Companies, where it used to
 * live — the policy's whole effect is on this pipeline.
 *
 * It is workspace-wide state, though, backed by `workspace_crm_settings` rather
 * than by anything on this screen. If a Settings→CRM page is ever added this
 * control belongs there and should *move*, not be copied: two controls writing
 * one row is how the two disagree.
 */
export function CapturePolicySelect() {
  const settingsQuery = useCrmGetSettingsQuery()
  const [updateSettings, state] = useCrmUpdateSettingsMutation()

  return (
    <>
      <label className="flex items-center gap-2 text-xs font-medium text-muted-foreground">
        Positive reply capture
        <Select
          wrapperClassName="w-auto"
          className="h-8 w-auto min-w-32 text-xs"
          value={settingsQuery.data?.auto_capture_policy ?? 'sent'}
          disabled={settingsQuery.isLoading || state.isLoading}
          onChange={(event) =>
            void updateSettings({ crmSettingsInput: { auto_capture_policy: event.target.value as AutoCapturePolicy } })
          }
        >
          <option value="sent">Sent campaigns</option>
          <option value="sent_and_received">Sent and received</option>
          <option value="off">Off</option>
        </Select>
      </label>
      {state.isError ? (
        <span role="alert" className="text-xs text-danger">
          {crmErrorMessage(state.error, 'The capture policy could not be updated.')}
        </span>
      ) : null}
    </>
  )
}
