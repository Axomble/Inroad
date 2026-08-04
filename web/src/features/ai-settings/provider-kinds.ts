// The per-kind connect-form schemas and picker copy. One table drives the
// whole "Add provider" flow: which fields a kind needs, which are secrets
// (sealed `credentials`) vs plain `config`, and how each validates — so a new
// kind is a new entry here, not a new form component.
import type { ComponentType } from 'react'
import { Boxes, Cable, Cloud, CloudCog } from 'lucide-react'
import type { AiProviderKind } from './api'
import { AnthropicIcon, GoogleIcon, OpenAiIcon } from './provider-icons'

export type ProviderFormField = {
  key: string
  label: string
  placeholder?: string
  helper?: string
  required: boolean
  /** true → sent in `credentials` (sealed); false → `config` (plain). */
  secret: boolean
  input: 'text' | 'password' | 'url' | 'json'
}

export type ProviderKindMeta = {
  kind: AiProviderKind
  title: string
  /** One-liner on the picker tile. */
  blurb: string
  group: 'direct' | 'cloud' | 'gateway'
  /** Short "where do I find this" line under the connect form. */
  helper?: string
  icon: ComponentType<{ className?: string }>
  fields: ProviderFormField[]
}

const apiKeyField = (placeholder: string): ProviderFormField => ({
  key: 'api_key',
  label: 'API key',
  placeholder,
  required: true,
  secret: true,
  input: 'password',
})

export const PROVIDER_KIND_GROUPS: readonly { id: ProviderKindMeta['group']; label: string }[] = [
  { id: 'direct', label: 'Direct' },
  { id: 'cloud', label: 'Via your cloud' },
  { id: 'gateway', label: 'Gateway' },
]

export const PROVIDER_KINDS: readonly ProviderKindMeta[] = [
  {
    kind: 'anthropic',
    title: 'Anthropic',
    blurb: 'Claude models with an API key',
    group: 'direct',
    helper: 'Create a key at console.anthropic.com → API keys.',
    icon: AnthropicIcon,
    fields: [apiKeyField('sk-ant-…')],
  },
  {
    kind: 'openai',
    title: 'OpenAI',
    blurb: 'GPT models with an API key',
    group: 'direct',
    helper: 'Create a key at platform.openai.com → API keys.',
    icon: OpenAiIcon,
    fields: [apiKeyField('sk-…')],
  },
  {
    kind: 'google',
    title: 'Google',
    blurb: 'Gemini models with an AI Studio key',
    group: 'direct',
    helper: 'Create a key at aistudio.google.com → Get API key.',
    icon: GoogleIcon,
    fields: [apiKeyField('AIza…')],
  },
  {
    kind: 'azure_openai',
    title: 'Azure OpenAI',
    blurb: 'Your Azure OpenAI resource and deployments',
    group: 'cloud',
    helper: 'Endpoint, key, and API version live under your resource’s "Keys and Endpoint" blade.',
    icon: Cloud,
    fields: [
      {
        key: 'endpoint',
        label: 'Resource endpoint',
        placeholder: 'https://my-resource.openai.azure.com',
        required: true,
        secret: false,
        input: 'url',
      },
      { key: 'api_version', label: 'API version', placeholder: '2024-10-21', required: true, secret: false, input: 'text' },
      apiKeyField('Azure API key'),
    ],
  },
  {
    kind: 'bedrock',
    title: 'AWS Bedrock',
    blurb: 'Claude and more through your AWS account',
    group: 'cloud',
    helper: 'Use an IAM user with bedrock:InvokeModel access in the region you pick.',
    icon: Boxes,
    fields: [
      { key: 'access_key_id', label: 'Access key ID', placeholder: 'AKIA…', required: true, secret: true, input: 'text' },
      { key: 'secret_access_key', label: 'Secret access key', required: true, secret: true, input: 'password' },
      { key: 'region', label: 'Region', placeholder: 'us-east-1', required: true, secret: false, input: 'text' },
    ],
  },
  {
    kind: 'vertex_anthropic',
    title: 'Vertex AI (Claude)',
    blurb: 'Claude through your Google Cloud project',
    group: 'cloud',
    helper: 'Paste a service-account key JSON with the Vertex AI User role.',
    icon: CloudCog,
    fields: [
      {
        key: 'service_account_json',
        label: 'Service-account JSON',
        placeholder: '{ "type": "service_account", … }',
        required: true,
        secret: true,
        input: 'json',
      },
      { key: 'project_id', label: 'Project ID', placeholder: 'my-gcp-project', required: true, secret: false, input: 'text' },
      { key: 'region', label: 'Region', placeholder: 'us-east5', required: true, secret: false, input: 'text' },
    ],
  },
  {
    kind: 'vertex_google',
    title: 'Vertex AI (Gemini)',
    blurb: 'Gemini through your Google Cloud project',
    group: 'cloud',
    helper: 'Paste a service-account key JSON with the Vertex AI User role.',
    icon: CloudCog,
    fields: [
      {
        key: 'service_account_json',
        label: 'Service-account JSON',
        placeholder: '{ "type": "service_account", … }',
        required: true,
        secret: true,
        input: 'json',
      },
      { key: 'project_id', label: 'Project ID', placeholder: 'my-gcp-project', required: true, secret: false, input: 'text' },
      { key: 'region', label: 'Region', placeholder: 'us-central1', required: true, secret: false, input: 'text' },
    ],
  },
  {
    kind: 'openai_compatible',
    title: 'OpenAI-compatible',
    blurb: 'OpenRouter, LiteLLM, Ollama, vLLM — any endpoint speaking the OpenAI API',
    group: 'gateway',
    helper: 'The 30-second path to any model you can reach: one base URL, optionally a key.',
    icon: Cable,
    fields: [
      {
        key: 'base_url',
        label: 'Base URL',
        placeholder: 'https://openrouter.ai/api/v1',
        helper: 'Include the version segment (usually /v1) — model discovery calls {base URL}/models.',
        required: true,
        secret: false,
        input: 'url',
      },
      {
        key: 'api_key',
        label: 'API key (optional)',
        helper: 'Open endpoints like a local Ollama need no key.',
        required: false,
        secret: true,
        input: 'password',
      },
    ],
  },
]

export const KIND_META: Record<AiProviderKind, ProviderKindMeta> = Object.fromEntries(
  PROVIDER_KINDS.map((meta) => [meta.kind, meta]),
) as Record<AiProviderKind, ProviderKindMeta>
