<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { ref } from 'vue'
import { useSessionsStore } from '../../stores/sessions'

const query = ref('')
const sessionsStore = useSessionsStore()

// Client-side name filter: update the shared query; the session list
// re-derives its groups reactively. No server round-trip, so grouping and
// starred/recent sections are preserved.
function onInput() {
  sessionsStore.setSearchQuery(query.value)
}

function clearSearch() {
  query.value = ''
  sessionsStore.setSearchQuery('')
}
</script>

<template>
  <div id="sidebar-search" class="visible">
    <!-- The icon and clear button are positioned against this wrapper rather
         than the padded outer box. Centring on the outer box put them halfway
         down its 2px/8px asymmetric padding, which is why the icon sat low. -->
    <div class="search-field">
      <span class="search-icon" aria-hidden="true">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="15" height="15">
          <circle cx="11" cy="11" r="7"/><path d="M21 21l-4.3-4.3" stroke-linecap="round"/>
        </svg>
      </span>
      <input
        v-model="query"
        type="text"
        placeholder="Search chats…"
        @input="onInput"
        @keydown.esc="clearSearch"
      />
      <button v-if="query" class="search-clear" @click="clearSearch" title="Clear" aria-label="Clear search">×</button>
    </div>
  </div>
</template>

<style scoped>
#sidebar-search { padding: 2px 12px 8px; }
.search-field { position: relative; display: flex; }
.search-icon {
  position: absolute; left: 10px; top: 50%; transform: translateY(-50%);
  color: var(--text2); display: inline-flex; pointer-events: none;
}
#sidebar-search input {
  width: 100%; padding: 8px 28px 8px 32px; border: 1px solid var(--border); border-radius: 8px;
  background: var(--surface2); color: var(--text); font-size: 13px; outline: none;
  transition: border-color .15s;
}
#sidebar-search input:focus { border-color: var(--accent); }
#sidebar-search input::placeholder { color: var(--text2); }
.search-clear {
  position: absolute; right: 8px; top: 50%; transform: translateY(-50%);
  background: none; border: none; color: var(--text2); cursor: pointer;
  font-size: 16px; line-height: 1; padding: 2px 5px; border-radius: 4px;
}
.search-clear:hover { color: var(--text); background: var(--surface3); }
</style>
