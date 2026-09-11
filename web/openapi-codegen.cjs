// RTK Query codegen config. Plain CommonJS, NOT TypeScript, on purpose.
//
// rtk-query-codegen-openapi only understands a .ts/.cts config if it can first
// register a TypeScript runner, and it tries exactly two: esbuild-runner, then
// ts-node. With a .cts config and neither available it prints
//
//   Encountered a TypeScript configfile, but neither esbuild-runner nor ts-node
//   are installed.
//
// and exits 1. That message is misleading whenever ts-node IS installed but
// fails to register: the CLI wraps `require('ts-node').register(...)` in a bare
// `catch {}`, so the real error is swallowed and you get the "not installed"
// line instead. Under typescript 7 that is what happens — ts-node 10.9.2 reaches
// into a TS-internal API that moved, and throws
// "Cannot read properties of undefined (reading 'fileExists')".
//
// That is why the Docker image builds (Dockerfile.web / Dockerfile.api) had
// never once succeeded: they run `npm ci` from the lockfile, which pins
// typescript 7.0.2, so `npm run gen:api` always died in the web-build stage.
// A developer machine with an older typescript still in node_modules ran it
// fine, which is what kept this hidden.
//
// A .cjs config sidesteps the whole mechanism: no runner to register, nothing
// to swallow an error, and no coupling between codegen and whatever typescript
// version the repo happens to pin next. The type safety lost here is one object
// literal against ConfigFile — the generated output is still fully typed, and
// `tsc -b` still checks every consumer of it.
/** @type {import('@rtk-query/codegen-openapi').ConfigFile} */
const config = {
  schemaFile: '../api/openapi.yaml',
  apiFile: './src/store/empty-api.ts',
  apiImport: 'emptyApi',
  outputFile: './src/store/api.ts',
  exportName: 'api',
  hooks: true,
}
module.exports = config
