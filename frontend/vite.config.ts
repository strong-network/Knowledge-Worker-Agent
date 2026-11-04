// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { resolve } from 'path'

export default defineConfig({
  plugins: [vue()],
  root: '.',
  // mermaid's ELK layouts need elkjs, which is under the EPL-2.0: leave it out.
  resolve: {
    alias: { 'elkjs/lib/elk.bundled.js': resolve(import.meta.dirname, 'src/utils/noElk.ts') },
  },
  build: {
    outDir: resolve(import.meta.dirname, '../cmd/server/html'),
    emptyOutDir: true,
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8765',
        changeOrigin: true,
      },
      '/healthz': {
        target: 'http://127.0.0.1:8765',
        changeOrigin: true,
      },
    },
  },
})
