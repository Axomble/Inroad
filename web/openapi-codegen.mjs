// Plain JS on purpose. This config used to be `.cts`, which made the codegen CLI
// reach for a TypeScript loader — and when `typescript` went to 7.x, `ts-node`
// 10 could no longer load it. The CLI reports that as "neither esbuild-runner
// nor ts-node are installed", which is misleading: ts-node WAS installed, its
// register hook just threw.
//
// That broke `npm run gen:api`, which deploy/docker/Dockerfile.{api,web} run at
// image build time, so every container image build failed. Nothing in ci.yml
// ran gen:api, so it stayed broken and invisible until someone looked at the
// Images workflow.
//
// A nine-line config is not worth a build-time dependency on a TypeScript
// loader that a future compiler bump can break again. The JSDoc annotation
// keeps editor type-checking against the real ConfigFile type.
/** @type {import('@rtk-query/codegen-openapi').ConfigFile} */
const config = {
  schemaFile: '../api/openapi.yaml',
  apiFile: './src/store/empty-api.ts',
  apiImport: 'emptyApi',
  outputFile: './src/store/api.ts',
  exportName: 'api',
  hooks: true,
}
export default config
