// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import DocWindowApp from './DocWindowApp.vue'
import GuestApp from './GuestApp.vue'
import './style.css'
import 'highlight.js/styles/github-dark.css'
import { registerServiceWorker } from './pwa'

// `?doc=<path>` boots the pop-out document window instead of the chat app.
// A query parameter rather than a path like /viewer because the Go
// handler serves index.html only for "/" — every other path 404s.
const docPath = new URLSearchParams(window.location.search).get('doc')

// `/s/<id>` is only ever served by the guest listener, so the path
// alone says this is a coworker's view of a shared chat.
const isGuest = window.location.pathname.startsWith('/s/')

const app = createApp(isGuest ? GuestApp : docPath ? DocWindowApp : App)
app.use(createPinia())
app.mount('#app')

// Only the chat window manages the service worker. Registering it from the
// document window would also install its `controllerchange` reload handler,
// and reloading a window that may hold unsaved edits just to pick up a new
// bundle is a bad trade. The guest listener does not serve the worker at all.
if (!docPath && !isGuest) registerServiceWorker()
