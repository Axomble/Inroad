import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { tanstackRouter } from '@tanstack/router-plugin/vite'

// Where the dev server proxies /api and /oauth2. Defaults to a natively-run API
// on the host; the docker dev stack sets this to the `api` service.
const apiProxyTarget = process.env.VITE_API_PROXY_TARGET ?? 'http://localhost:8080'

export default defineConfig({
  plugins: [
    tanstackRouter({
      target: 'react',
      autoCodeSplitting: true,
      // Route-adjacent tests live beside their screens but are not routes.
      routeFileIgnorePattern: '\\.test\\.tsx$',
    }),
    react(),
    tailwindcss(),
  ],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    // Bind on all interfaces so the port is reachable when Vite runs inside the
    // dev container; harmless when running natively.
    host: true,
    // `/oauth2` is the OAuth 2.1 provider mounted at the API server root (a sibling
    // of `/api/v1`), so the consent screen's fetches must be proxied to the API too.
    //
    // The target is configurable because the API is not always on localhost: in
    // the docker dev stack it answers to the service name `api`.
    proxy: {
      '/api': apiProxyTarget,
      '/oauth2': apiProxyTarget,
      // Mailbox-connect OAuth callbacks are server routes (the API owns
      // /oauth/<provider>/callback — see internal/platform/httpx/spa.go). Proxy
      // them individually rather than all of /oauth, because /oauth/consent is
      // an SPA route that Vite must keep serving itself.
      '/oauth/google/callback': apiProxyTarget,
      '/oauth/microsoft/callback': apiProxyTarget,
    },
  },
})
