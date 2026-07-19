import type { ConfigFile } from '@rtk-query/codegen-openapi'

const config: ConfigFile = {
  schemaFile: '../api/openapi.yaml',
  apiFile: './src/store/empty-api.ts',
  apiImport: 'emptyApi',
  outputFile: './src/store/api.ts',
  exportName: 'api',
  hooks: true,
}
export default config
