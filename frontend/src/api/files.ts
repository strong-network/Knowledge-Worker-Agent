// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import {
  browseFiles, browseEntries, viewFile, saveFile, createDirectory, deletePath, renamePath,
  uploadFiles, downloadFileUrl, rawFileUrl,
  type FileEntry, type FileViewResponse, type UploadResponse,
} from './index'

// How the file components reach files, so one set of components serves the
// owner (absolute paths, every action) and a guest of a shared chat (paths
// inside the chat's folder, reads and additive writes only).
export interface FileApi {
  browse(path: string): Promise<FileEntry[]>
  view(path: string): Promise<FileViewResponse>
  save(path: string, content: string): Promise<void>
  createDirectory(path: string): Promise<string>
  upload(files: FileList | File[], targetDir: string): Promise<UploadResponse>
  downloadUrl(path: string): string
  rawUrl(path: string): string
  // Absent for guests, and so are their controls.
  rename?: (src: string, dest: string) => Promise<void>
  remove?: (path: string) => Promise<void>
}

export const ownerFileApi: FileApi = {
  browse: browseFiles,
  view: viewFile,
  save: saveFile,
  createDirectory: (path) => createDirectory(path),
  upload: (files, targetDir) => uploadFiles(files, targetDir),
  downloadUrl: downloadFileUrl,
  rawUrl: rawFileUrl,
  rename: async (src, dest) => { await renamePath(src, dest) },
  remove: deletePath,
}

// The guest routes answer failures with {error}, written for the guest.
async function failure(res: Response, fallback: string): Promise<never> {
  let msg = ''
  try { msg = (await res.json())?.error || '' } catch { /* not JSON */ }
  throw new Error(msg || fallback)
}

export function guestFileApi(sessionId: string): FileApi {
  const base = `/guest/api/sessions/${encodeURIComponent(sessionId)}/files`
  const at = (verb: string, path: string) => `${base}/${verb}?path=${encodeURIComponent(path)}`
  async function post<T>(verb: string, body: unknown, fallback: string): Promise<T> {
    const res = await fetch(`${base}/${verb}`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body),
    })
    if (!res.ok) await failure(res, fallback)
    return res.json()
  }
  return {
    async browse(path) {
      const res = await fetch(at('browse', path))
      if (!res.ok) await failure(res, 'Could not open the folder')
      return browseEntries(await res.json())
    },
    async view(path) {
      const res = await fetch(at('view', path))
      if (!res.ok) await failure(res, 'Could not open the file')
      return res.json()
    },
    async save(path, content) {
      await post('save', { path, content }, 'Could not save the file')
    },
    async createDirectory(path) {
      return (await post<{ path: string }>('directory', { path }, 'Could not create the folder')).path
    },
    async upload(files, targetDir) {
      const form = new FormData()
      form.append('target_dir', targetDir)
      for (const f of Array.from(files)) form.append('files', f)
      const res = await fetch(`${base}/upload`, { method: 'POST', body: form })
      if (!res.ok) await failure(res, 'Upload failed')
      return res.json()
    },
    downloadUrl: (path) => at('download', path),
    rawUrl: (path) => at('raw', path),
  }
}
