// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

/// <reference types="vite/client" />

declare module '*.vue' {
  import type { DefineComponent } from 'vue'
  const component: DefineComponent<{}, {}, any>
  export default component
}
