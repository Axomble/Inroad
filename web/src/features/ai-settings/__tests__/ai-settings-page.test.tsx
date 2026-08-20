import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import { beforeAll, beforeEach, afterEach, expect, test, vi } from 'vitest'
import { renderWithProviders } from '@/test/render-with-providers'
import { AiSettingsPage } from '../ai-settings-page'
import type { AiDiscoveryResult, AiModel, AiProvider, AiSettings } from '../api'

// Radix AlertDialog touches pointer + scroll APIs jsdom doesn't implement.
beforeAll(() => {
  const proto = Element.prototype as unknown as Record<string, unknown>
  proto.hasPointerCapture ??= () => false
  proto.setPointerCapture ??= () => {}
  proto.releasePointerCapture ??= () => {}
  proto.scrollIntoView ??= () => {}
})

const jsonHeaders = { 'content-type': 'application/json' }
const admin = { auth: { role: 'admin', status: 'authed' as const, activeWorkspaceId: 'w1' } }

let providers: AiProvider[]
let models: AiModel[]
let settings: AiSettings
let settingsPutStatus: number
let discoverStatus: number
let discoverResponse: AiDiscoveryResult
let lastProviderPost: Record<string, unknown> | null
let lastProviderPut: { id: string; body: Record<string, unknown> } | null
let modelPosts: Record<string, unknown>[]
let lastSettingsPut: AiSettings | null
let deletedProviderId: string | null
let deletedModelId: string | null
let nextId: number

const anthropicRow = (): AiProvider => ({
  id: 'p-anthropic',
  kind: 'anthropic',
  display_name: '',
  config: {},
  configured: true,
  key_prefix: 'sk-ant-',
  created_at: new Date().toISOString(),
  updated_at: new Date().toISOString(),
})

const openrouterRow = (): AiProvider => ({
  id: 'g1',
  kind: 'openai_compatible',
  display_name: 'OpenRouter',
  config: { base_url: 'https://openrouter.ai/api/v1' },
  configured: true,
  key_prefix: 'sk-or-v',
  created_at: new Date().toISOString(),
  updated_at: new Date().toISOString(),
})

const bedrockRow = (): AiProvider => ({
  id: 'b1',
  kind: 'bedrock',
  display_name: '',
  config: { region: 'us-east-1' },
  configured: true,
  key_prefix: 'AKIAEXA',
  created_at: new Date().toISOString(),
  updated_at: new Date().toISOString(),
})

// Row-scoped ids per the final contract: "<provider_row_uuid>/<name>", with
// provider_id always present (catalog entries hang off the native row).
const claudeModels = (): AiModel[] => [
  {
    id: 'p-anthropic/claude-smart',
    provider_id: 'p-anthropic',
    kind: 'anthropic',
    name: 'claude-smart',
    label: 'Claude Smart',
    context_window_tokens: 200000,
    max_output_tokens: 64000,
    supports_reasoning: true,
    source: 'catalog',
    custom_model_id: null,
    input_cost_per_mtok: null,
    output_cost_per_mtok: null,
    enabled: true,
  },
  {
    id: 'p-anthropic/claude-fast',
    provider_id: 'p-anthropic',
    kind: 'anthropic',
    name: 'claude-fast',
    label: 'Claude Fast',
    context_window_tokens: 200000,
    max_output_tokens: 32000,
    supports_reasoning: false,
    source: 'catalog',
    custom_model_id: null,
    input_cost_per_mtok: null,
    output_cost_per_mtok: null,
    enabled: true,
  },
]

const maverickModel = (): AiModel => ({
  id: 'g1/meta-llama/llama-4-maverick',
  provider_id: 'g1',
  kind: 'openai_compatible',
  name: 'meta-llama/llama-4-maverick',
  label: 'Llama 4 Maverick',
  context_window_tokens: 1000000,
  max_output_tokens: 16000,
  supports_reasoning: false,
  source: 'custom',
  custom_model_id: 'mvk1',
  input_cost_per_mtok: 0.2,
  output_cost_per_mtok: 0.6,
  enabled: true,
})

beforeEach(() => {
  // Default world: one Anthropic row with two catalog models. Tests seed
  // gateways/bedrock/custom models on top.
  providers = [anthropicRow()]
  models = claudeModels()
  settings = {
    default_smart_model: 'default-smart-model',
    default_fast_model: 'default-fast-model',
    enabled_model_ids: [],
    additional_instructions: '',
  }
  settingsPutStatus = 200
  discoverStatus = 200
  discoverResponse = {
    supported: true,
    models: [
      {
        name: 'meta-llama/llama-4-maverick',
        label: 'Llama 4 Maverick',
        context_window_tokens: 1000000,
        input_cost_per_mtok: 0.2,
        output_cost_per_mtok: 0.6,
      },
      { name: 'qwen3:8b' },
    ],
  }
  lastProviderPost = null
  lastProviderPut = null
  modelPosts = []
  lastSettingsPut = null
  deletedProviderId = null
  deletedModelId = null
  nextId = 1

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const request = input instanceof Request ? input : new Request(input)
      const url = request.url
      const method = request.method

      if (method === 'POST' && url.endsWith('/discover')) {
        if (discoverStatus !== 200) {
          return new Response(JSON.stringify({ error: 'unreachable' }), { status: discoverStatus, headers: jsonHeaders })
        }
        return new Response(JSON.stringify(discoverResponse), { status: 200, headers: jsonHeaders })
      }

      if (url.includes('/ai/providers/')) {
        const id = url.slice(url.lastIndexOf('/') + 1)
        if (method === 'DELETE') {
          deletedProviderId = id
          providers = providers.filter((p) => p.id !== id)
          models = models.filter((m) => m.provider_id !== id)
          return new Response(null, { status: 204 })
        }
        // PUT — edit config/name; credentials only when re-entered.
        const body = JSON.parse(await request.text()) as {
          display_name?: string
          config?: Record<string, string>
          credentials?: Record<string, string>
        }
        lastProviderPut = { id, body }
        providers = providers.map((p) =>
          p.id === id
            ? {
                ...p,
                display_name: body.display_name ?? p.display_name,
                config: body.config ?? p.config,
                updated_at: new Date().toISOString(),
              }
            : p,
        )
        return new Response(JSON.stringify(providers.find((p) => p.id === id)), { status: 200, headers: jsonHeaders })
      }

      if (url.includes('/ai/providers')) {
        if (method === 'POST') {
          const body = JSON.parse(await request.text()) as {
            kind: AiProvider['kind']
            display_name?: string
            credentials: Record<string, string>
            config: Record<string, string>
          }
          lastProviderPost = body as unknown as Record<string, unknown>
          const secret = body.credentials.api_key ?? body.credentials.access_key_id ?? ''
          const record: AiProvider = {
            id: `p-new-${nextId++}`,
            kind: body.kind,
            display_name: body.display_name ?? '',
            config: body.config,
            configured: true,
            key_prefix: secret.slice(0, 7),
            created_at: new Date().toISOString(),
            updated_at: new Date().toISOString(),
          }
          providers = [...providers, record]
          return new Response(JSON.stringify(record), { status: 201, headers: jsonHeaders })
        }
        return new Response(JSON.stringify({ providers }), { status: 200, headers: jsonHeaders })
      }

      if (url.includes('/ai/models/')) {
        // The bare custom_model_id — never the "<row>/<name>" display id.
        const rowId = url.slice(url.lastIndexOf('/') + 1)
        deletedModelId = rowId
        models = models.filter((m) => m.custom_model_id !== rowId)
        return new Response(null, { status: 204 })
      }

      if (url.includes('/ai/models')) {
        if (method === 'POST') {
          const body = JSON.parse(await request.text()) as Record<string, unknown>
          modelPosts = [...modelPosts, body]
          const created: AiModel = {
            id: `${String(body.provider_id)}/${String(body.name)}`,
            provider_id: String(body.provider_id),
            kind: 'openai_compatible',
            name: String(body.name),
            label: String(body.label),
            context_window_tokens: Number(body.context_window_tokens),
            max_output_tokens: Number(body.max_output_tokens),
            supports_reasoning: Boolean(body.supports_reasoning),
            source: 'custom',
            custom_model_id: `cm-${nextId++}`,
            input_cost_per_mtok: typeof body.input_cost_per_mtok === 'number' ? body.input_cost_per_mtok : null,
            output_cost_per_mtok: typeof body.output_cost_per_mtok === 'number' ? body.output_cost_per_mtok : null,
            enabled: true,
          }
          models = [...models, created]
          return new Response(JSON.stringify(created), { status: 201, headers: jsonHeaders })
        }
        return new Response(JSON.stringify({ models }), { status: 200, headers: jsonHeaders })
      }

      if (url.includes('/ai/settings')) {
        if (method === 'PUT') {
          if (settingsPutStatus !== 200) {
            return new Response(JSON.stringify({ error: 'boom' }), { status: settingsPutStatus, headers: jsonHeaders })
          }
          settings = JSON.parse(await request.text()) as AiSettings
          lastSettingsPut = settings
          return new Response(JSON.stringify(settings), { status: 200, headers: jsonHeaders })
        }
        return new Response(JSON.stringify(settings), { status: 200, headers: jsonHeaders })
      }
      return new Response('not found', { status: 404 })
    }),
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

test('non-admins get an admins-only state, not the forms', async () => {
  renderWithProviders(<AiSettingsPage />, {
    preloadedState: { auth: { role: 'member', status: 'authed', activeWorkspaceId: 'w1' } },
  })

  expect(await screen.findByText('Admins only')).toBeInTheDocument()
  expect(screen.queryByText('Providers')).not.toBeInTheDocument()
})

test('a workspace with no providers gets the zero-state; its doors open the kind picker', async () => {
  providers = []
  models = []
  renderWithProviders(<AiSettingsPage />, { preloadedState: admin })

  expect(await screen.findByText('Connect a provider to turn on the assistant')).toBeInTheDocument()
  expect(screen.getByText('Through your cloud')).toBeInTheDocument()

  fireEvent.click(screen.getByRole('button', { name: /a direct api key/i }))
  const dialog = within(await screen.findByRole('alertdialog'))
  expect(dialog.getByText('Add an AI provider')).toBeInTheDocument()
  // The picker is grouped: Direct · Via your cloud · Gateway.
  expect(dialog.getByText('Direct')).toBeInTheDocument()
  expect(dialog.getByText('Via your cloud')).toBeInTheDocument()
  expect(dialog.getByText('Gateway')).toBeInTheDocument()
  expect(dialog.getByRole('button', { name: /aws bedrock/i })).toBeInTheDocument()
})

test('connecting a direct provider POSTs kind + credentials and shows the masked prefix', async () => {
  providers = []
  models = []
  renderWithProviders(<AiSettingsPage />, { preloadedState: admin })

  fireEvent.click(await screen.findByRole('button', { name: /add provider/i }))
  let dialog = within(await screen.findByRole('alertdialog'))
  fireEvent.click(dialog.getByRole('button', { name: /anthropic/i }))

  dialog = within(screen.getByRole('alertdialog'))
  const connect = dialog.getByRole('button', { name: /connect anthropic/i })
  expect(connect).toBeDisabled()

  fireEvent.change(dialog.getByLabelText('API key'), { target: { value: 'sk-ant-test-1234' } })
  expect(connect).toBeEnabled()
  fireEvent.click(connect)

  await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/anthropic connected/i))
  expect(lastProviderPost).toEqual({
    kind: 'anthropic',
    credentials: { api_key: 'sk-ant-test-1234' },
    config: {},
  })
  // The refetched row shows only the masked head — never the raw key.
  expect(await screen.findByText('sk-ant-…')).toBeInTheDocument()
  expect(screen.queryByDisplayValue('sk-ant-test-1234')).not.toBeInTheDocument()
})

test('the Bedrock form requires all three fields and routes secrets vs config correctly', async () => {
  renderWithProviders(<AiSettingsPage />, { preloadedState: admin })

  fireEvent.click(await screen.findByRole('button', { name: /add provider/i }))
  let dialog = within(await screen.findByRole('alertdialog'))
  fireEvent.click(dialog.getByRole('button', { name: /aws bedrock/i }))

  dialog = within(screen.getByRole('alertdialog'))
  const connect = dialog.getByRole('button', { name: /connect aws bedrock/i })

  fireEvent.change(dialog.getByLabelText('Access key ID'), { target: { value: 'AKIAEXAMPLE' } })
  expect(connect).toBeDisabled()
  fireEvent.change(dialog.getByLabelText('Secret access key'), { target: { value: 'shhh-secret' } })
  expect(connect).toBeDisabled() // region still missing
  fireEvent.change(dialog.getByLabelText('Region'), { target: { value: 'us-east-1' } })
  expect(connect).toBeEnabled()
  fireEvent.click(connect)

  await waitFor(() =>
    expect(lastProviderPost).toEqual({
      kind: 'bedrock',
      credentials: { access_key_id: 'AKIAEXAMPLE', secret_access_key: 'shhh-secret' },
      config: { region: 'us-east-1' },
    }),
  )
})

test('the Vertex form rejects non-JSON service accounts', async () => {
  renderWithProviders(<AiSettingsPage />, { preloadedState: admin })

  fireEvent.click(await screen.findByRole('button', { name: /add provider/i }))
  let dialog = within(await screen.findByRole('alertdialog'))
  fireEvent.click(dialog.getByRole('button', { name: /vertex ai \(claude\)/i }))

  dialog = within(screen.getByRole('alertdialog'))
  const connect = dialog.getByRole('button', { name: /connect vertex ai \(claude\)/i })

  fireEvent.change(dialog.getByLabelText('Project ID'), { target: { value: 'my-project' } })
  fireEvent.change(dialog.getByLabelText('Region'), { target: { value: 'us-east5' } })
  fireEvent.change(dialog.getByLabelText('Service-account JSON'), { target: { value: 'not json at all' } })
  expect(connect).toBeDisabled()

  fireEvent.change(dialog.getByLabelText('Service-account JSON'), {
    target: { value: '{"type":"service_account","project_id":"my-project"}' },
  })
  expect(connect).toBeEnabled()
  fireEvent.click(connect)

  await waitFor(() =>
    expect(lastProviderPost).toEqual({
      kind: 'vertex_anthropic',
      credentials: { service_account_json: '{"type":"service_account","project_id":"my-project"}' },
      config: { project_id: 'my-project', region: 'us-east5' },
    }),
  )
})

test('connecting a gateway (key optional) chains straight into model discovery', async () => {
  renderWithProviders(<AiSettingsPage />, { preloadedState: admin })

  fireEvent.click(await screen.findByRole('button', { name: /add provider/i }))
  let dialog = within(await screen.findByRole('alertdialog'))
  fireEvent.click(dialog.getByRole('button', { name: /openai-compatible/i }))

  dialog = within(screen.getByRole('alertdialog'))
  const connect = dialog.getByRole('button', { name: /connect openai-compatible/i })
  expect(connect).toBeDisabled()

  fireEvent.change(dialog.getByLabelText('Base URL'), { target: { value: 'https://openrouter.ai/api/v1' } })
  expect(connect).toBeEnabled() // API key stays optional
  fireEvent.click(connect)

  // The discovery dialog opens on the fresh row, candidates fetched.
  expect(await screen.findByText('Models on openrouter.ai')).toBeInTheDocument()
  expect(await screen.findByText('Llama 4 Maverick')).toBeInTheDocument()
  expect(lastProviderPost).toEqual({
    kind: 'openai_compatible',
    credentials: {},
    config: { base_url: 'https://openrouter.ai/api/v1' },
  })
})

test('discovery: selecting candidates creates them with metadata and costs passed through', async () => {
  providers = [...providers, openrouterRow()]
  renderWithProviders(<AiSettingsPage />, { preloadedState: admin })

  fireEvent.click(await screen.findByRole('button', { name: /fetch models from openrouter/i }))
  const dialog = within(await screen.findByRole('alertdialog'))

  fireEvent.click(await dialog.findByRole('checkbox', { name: /select llama 4 maverick/i }))
  fireEvent.click(dialog.getByRole('button', { name: /add 1 selected/i }))

  await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/1 model added to openrouter/i))
  expect(modelPosts).toEqual([
    {
      provider_id: 'g1',
      name: 'meta-llama/llama-4-maverick',
      label: 'Llama 4 Maverick',
      context_window_tokens: 1000000,
      max_output_tokens: 16000,
      supports_reasoning: false,
      input_cost_per_mtok: 0.2,
      output_cost_per_mtok: 0.6,
    },
  ])
  // The created model lands in the row's list and the Models section.
  expect((await screen.findAllByText('Llama 4 Maverick')).length).toBeGreaterThanOrEqual(2)
})

test('discovery: a bare-id candidate hands off to the manual form prefilled', async () => {
  providers = [...providers, openrouterRow()]
  renderWithProviders(<AiSettingsPage />, { preloadedState: admin })

  fireEvent.click(await screen.findByRole('button', { name: /fetch models from openrouter/i }))
  const discovery = within(await screen.findByRole('alertdialog'))
  fireEvent.click(await discovery.findByRole('button', { name: /add qwen3:8b manually/i }))

  // Discovery closes; the manual dialog opens seeded with the bare name and
  // editable defaults.
  const manual = within(await screen.findByRole('alertdialog'))
  expect(manual.getByText(/add a model to openrouter/i)).toBeInTheDocument()
  expect(manual.getByLabelText('Model name')).toHaveValue('qwen3:8b')
  expect(manual.getByLabelText('Context window (tokens)')).toHaveValue(128000)
})

test('discovery failure shows an inline error with retry; retry recovers', async () => {
  providers = [...providers, openrouterRow()]
  discoverStatus = 502
  renderWithProviders(<AiSettingsPage />, { preloadedState: admin })

  fireEvent.click(await screen.findByRole('button', { name: /fetch models from openrouter/i }))
  const dialog = within(await screen.findByRole('alertdialog'))

  expect(await dialog.findByRole('alert')).toHaveTextContent(/couldn't reach the provider/i)
  // Manual entry stays available even while discovery is down.
  expect(dialog.getByRole('button', { name: /add manually/i })).toBeInTheDocument()

  discoverStatus = 200
  fireEvent.click(dialog.getByRole('button', { name: /^retry$/i }))
  expect(await dialog.findByText('Llama 4 Maverick')).toBeInTheDocument()
})

test('discovery on an unsupported kind points straight at manual entry', async () => {
  providers = [...providers, bedrockRow()]
  discoverResponse = { supported: false, models: [] }
  renderWithProviders(<AiSettingsPage />, { preloadedState: admin })

  fireEvent.click(await screen.findByRole('button', { name: /fetch models from aws bedrock/i }))
  const dialog = within(await screen.findByRole('alertdialog'))

  expect(await dialog.findByText(/arrives with the runtime/i)).toBeInTheDocument()
  expect(dialog.getByRole('button', { name: /add manually/i })).toBeInTheDocument()
})

test('deleting a custom model confirms first, then removes the row everywhere', async () => {
  providers = [...providers, openrouterRow()]
  models = [...models, maverickModel()]
  renderWithProviders(<AiSettingsPage />, { preloadedState: admin })

  fireEvent.click(await screen.findByRole('button', { name: /remove model llama 4 maverick/i }))
  expect(await screen.findByText(/remove the model “llama 4 maverick”\?/i)).toBeInTheDocument()

  fireEvent.click(screen.getByRole('button', { name: /^remove model$/i }))

  await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/model “llama 4 maverick” removed/i))
  expect(deletedModelId).toBe('mvk1')
  await waitFor(() => expect(screen.queryByRole('checkbox', { name: /llama 4 maverick/i })).not.toBeInTheDocument())
})

test('removing a provider confirms first, then deletes by id', async () => {
  renderWithProviders(<AiSettingsPage />, { preloadedState: admin })

  fireEvent.click(await screen.findByRole('button', { name: /^remove anthropic$/i }))
  expect(await screen.findByText(/remove anthropic\?/i)).toBeInTheDocument()

  fireEvent.click(screen.getByRole('button', { name: /^remove provider$/i }))

  await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/anthropic removed/i))
  expect(deletedProviderId).toBe('p-anthropic')
})

test('editing a provider without re-entering the key PUTs no credentials', async () => {
  renderWithProviders(<AiSettingsPage />, { preloadedState: admin })

  fireEvent.click(await screen.findByRole('button', { name: /^edit anthropic$/i }))
  const dialog = within(await screen.findByRole('alertdialog'))

  // The secret field advertises that blank keeps the stored key.
  expect(dialog.getByLabelText(/api key \(leave blank to keep\)/i)).toHaveValue('')
  fireEvent.change(dialog.getByLabelText(/display name/i), { target: { value: 'Prod Anthropic' } })
  fireEvent.click(dialog.getByRole('button', { name: /^save changes$/i }))

  await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/anthropic updated/i))
  expect(lastProviderPut).toEqual({
    id: 'p-anthropic',
    body: { display_name: 'Prod Anthropic', config: {} },
  })
})

test('an empty model selection is explained as "all enabled"; checking models restricts and saves the ids', async () => {
  renderWithProviders(<AiSettingsPage />, { preloadedState: admin })

  expect(await screen.findByText(/every configured model is enabled/i)).toBeInTheDocument()

  fireEvent.click(screen.getByRole('checkbox', { name: /claude smart/i }))
  expect(screen.getByText(/1 of 2 models enabled/i)).toBeInTheDocument()

  fireEvent.click(screen.getByRole('button', { name: /save changes/i }))

  await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent(/ai settings saved/i))
  expect(lastSettingsPut?.enabled_model_ids).toEqual(['p-anthropic/claude-smart'])
})

test('default pickers offer the Auto sentinel and enabled models; a chosen model id is saved', async () => {
  renderWithProviders(<AiSettingsPage />, { preloadedState: admin })

  const smartSelect = (await screen.findByLabelText('Default smart model')) as HTMLSelectElement
  expect(smartSelect.value).toBe('default-smart-model')

  const optionLabels = Array.from(smartSelect.options).map((o) => o.textContent)
  expect(optionLabels).toContain('Auto (recommended)')
  expect(optionLabels).toContain('Claude Smart')

  fireEvent.change(smartSelect, { target: { value: 'p-anthropic/claude-fast' } })
  fireEvent.click(screen.getByRole('button', { name: /save changes/i }))

  await waitFor(() => expect(lastSettingsPut?.default_smart_model).toBe('p-anthropic/claude-fast'))
  expect(lastSettingsPut?.default_fast_model).toBe('default-fast-model')
})

test('save is disabled until something changes, and a failed save surfaces an error banner', async () => {
  settingsPutStatus = 500
  renderWithProviders(<AiSettingsPage />, { preloadedState: admin })

  const save = await screen.findByRole('button', { name: /save changes/i })
  expect(save).toBeDisabled()

  fireEvent.change(screen.getByLabelText(/workspace instructions/i), {
    target: { value: 'Write like a human.' },
  })
  expect(save).toBeEnabled()
  fireEvent.click(save)

  await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent(/couldn't save ai settings/i))
})
