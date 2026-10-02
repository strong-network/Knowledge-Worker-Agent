// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import { ref } from 'vue'

const notificationsSupported = typeof window !== 'undefined' && 'Notification' in window
const notificationPermission = ref<NotificationPermission>(
  notificationsSupported ? Notification.permission : 'denied'
)
const notificationsEnabled = ref(notificationPermission.value === 'granted')

interface NotifyOptions {
  requireBlur?: boolean
  toast?: boolean
}

export function useNotifications() {
  async function enable(): Promise<boolean> {
    if (!notificationsSupported) return false
    if (Notification.permission === 'granted') {
      notificationPermission.value = 'granted'
      notificationsEnabled.value = true
      return true
    }
    if (Notification.permission === 'denied') {
      notificationPermission.value = 'denied'
      notificationsEnabled.value = false
      return false
    }

    const permission = await Notification.requestPermission()
    notificationPermission.value = permission
    notificationsEnabled.value = permission === 'granted'
    return notificationsEnabled.value
  }

  function notify(title: string, body?: string, options: NotifyOptions = {}) {
    const { requireBlur = true, toast = false } = options

    if (toast) {
      ;(window as any).showToast?.(body ? `${title}: ${body}` : title)
    }

    if (!notificationsSupported || !notificationsEnabled.value) return
    if (requireBlur && document.hasFocus()) return
    new Notification(title, { body, icon: '/favicon.ico' })
  }

  return { notificationsSupported, notificationPermission, notificationsEnabled, enable, notify }
}
