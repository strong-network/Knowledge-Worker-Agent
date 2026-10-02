// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Triggering a file download without opening a browser window.
//
// The obvious `window.open(url)` is wrong here. The server answers the download
// endpoints with `Content-Disposition: attachment`, so the browser downloads the
// response and the window it was told to open has nothing to render — it just
// sits there blank. In a normal tab that flashes past unnoticed; but this app is
// installed as a standalone PWA (`"display": "standalone"`), where `window.open`
// spawns a whole separate *app window*. The user gets an empty grey copy of the
// app on top of the real one, with the save dialog over it.
//
// An anchor carrying the `download` attribute asks for the same bytes with no
// browsing context at all, so nothing is opened and the app is left alone.
export function triggerDownload(url: string): void {
  const a = document.createElement('a')
  a.href = url
  // Empty on purpose: for a same-origin response this defers to the filename
  // the server already sends in Content-Disposition, rather than second-
  // guessing it here (and getting folder .zip names wrong).
  a.download = ''
  a.rel = 'noopener'
  // Firefox only dispatches the click for an anchor that is in the document.
  a.style.display = 'none'
  document.body.appendChild(a)
  a.click()
  a.remove()
}
