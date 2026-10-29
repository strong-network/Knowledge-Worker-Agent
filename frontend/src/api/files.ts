// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import {
  browseFiles, browseEntries, viewFile, saveFile, createDirectory, deletePath, renamePath,
  duplicatePath, uploadFiles, downloadFileUrl, rawFileUrl, officePreviewUrl, previewLink,
  type FileEntry, type FileViewResponse, type UploadResponse, type PreviewLink,
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
  // Absent for guests, and so are their controls. Rename also moves.
  rename?: (src: string, dest: string) => Promise<void>
  remove?: (path: string) => Promise<void>
  duplicate?: (path: string) => Promise<string>
  // Absent for guests, whose office files show the download instead.
  officePreviewUrl?: (path: string) => string
  // Absent for guests, whose HTML previews keep scripts off.
  previewLink?: (path: string) => Promise<PreviewLink>
  // A text file's earlier versions, oldest first. Absent for guests.
  versions?: (path: string) => Promise<FileVersion[]>
  versionText?: (path: string, v: number) => Promise<string>
  versionDownloadUrl?: (path: string, v: number) => string
}

// One version of a file. author is who made it: the agent, you, a guest
// (named by name), or '' when that isn't known.
export interface FileVersion {
  v: number
  at: string
  author: 'agent' | 'you' | 'guest' | ''
  name?: string
  size: number
}

// Sharing a file from the Files widget: the owner's only. Without share, the
// widget only marks what's shared (the project view, which has no chat).
export interface FileSharing {
  marker(path: string): { label: string; attention: boolean } | null
  // How many shared files are at path or inside it.
  sharedIn(path: string): number
  // The server has moved or stopped them: re-read what's shared.
  changed(): void
  // Whether a fix is making the file self-contained.
  fixing?: (path: string) => boolean
  canShare?: (entry: FileEntry) => boolean
  share?: (entry: FileEntry) => void
  // Why Share can't be used here, when share is absent but canShare isn't.
  shareBlocked?: string
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
  duplicate: duplicatePath,
  officePreviewUrl,
  previewLink,
  async versions(path) {
    const res = await fetch(`/api/files/versions?path=${encodeURIComponent(path)}`, { cache: 'no-store' })
    if (!res.ok) throw new Error("Couldn't load the file's versions")
    return (await res.json()).versions as FileVersion[]
  },
  async versionText(path, v) {
    const res = await fetch(versionUrl(path, v), { cache: 'no-store' })
    if (!res.ok) throw new Error(`Couldn't load v${v}`)
    return res.text()
  },
  versionDownloadUrl: (path, v) => `${versionUrl(path, v)}&download=1`,
}

function versionUrl(path: string, v: number): string {
  return `/api/files/version?path=${encodeURIComponent(path)}&v=${v}`
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
