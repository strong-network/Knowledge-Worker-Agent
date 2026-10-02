// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import { defineStore } from 'pinia'
import { ref } from 'vue'
import { useSessionsStore } from './sessions'

export interface AttachedFile {
  file: File
  name: string
  path: string
  previewUrl: string | null
  uploading: boolean
}

export const useUploadsStore = defineStore('uploads', () => {
  const attachedFiles = ref<AttachedFile[]>([])
  const dragOver = ref(false)

  function uploadDir(): string {
    const workspace = useSessionsStore().currentSession?.workspace || ''
    return workspace ? `${workspace.replace(/\/+$/, '')}/upload` : ''
  }

  async function handleFiles(files: File[]) {
    for (const file of files) {
      const isImage = file.type.startsWith('image/')
      const entry: AttachedFile = {
        file,
        name: file.name,
        path: '',
        previewUrl: isImage ? URL.createObjectURL(file) : null,
        uploading: true,
      }
      attachedFiles.value.push(entry)

      try {
        const fd = new FormData()
        const targetDir = uploadDir()
        if (targetDir) fd.append('target_dir', targetDir)
        fd.append('files', file)
        const response = await fetch('/api/files/upload', { method: 'POST', body: fd })
        const data = await response.json()
        if (data.uploaded && data.uploaded.length > 0) {
          entry.path = data.uploaded[0].path
        }
      } catch (err) {
        console.error('Upload failed:', err)
      } finally {
        entry.uploading = false
      }
    }
  }

  function removeFile(index: number) {
    const entry = attachedFiles.value[index]
    if (entry?.previewUrl) URL.revokeObjectURL(entry.previewUrl)
    attachedFiles.value.splice(index, 1)
  }

  function clear() {
    for (const f of attachedFiles.value) {
      if (f.previewUrl) URL.revokeObjectURL(f.previewUrl)
    }
    attachedFiles.value = []
  }

  return {
    attachedFiles,
    dragOver,
    handleFiles,
    removeFile,
    clear,
  }
})
