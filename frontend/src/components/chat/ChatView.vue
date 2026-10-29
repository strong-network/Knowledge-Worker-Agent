<!-- Copyright 2026 Citrix Systems, Inc. -->
<!-- SPDX-License-Identifier: Apache-2.0 -->

<script setup lang="ts">
import { computed, ref, watch, nextTick, onMounted, onBeforeUnmount } from 'vue'
import { useSessionsStore } from '../../stores/sessions'
import { useChatStore, isFollowingBottom } from '../../stores/chat'
import { useProvidersStore } from '../../stores/providers'
import { useUiStore } from '../../stores/ui'
import { useSearchStore } from '../../stores/search'
import { highlightIn, clearHighlights } from '../../utils/searchHighlight'
import ChatHeader from './ChatHeader.vue'
import MessageBubble from './MessageBubble.vue'
import EmptyState from './EmptyState.vue'
import NewChatGreeting from './NewChatGreeting.vue'
import PromptInput from './PromptInput.vue'
import QuestionCard from './QuestionCard.vue'
import PermissionCard from './PermissionCard.vue'
import WorkspaceFilePanel from './WorkspaceFilePanel.vue'
import TimerIcon from '../TimerIcon.vue'
import { vTip } from '../../directives/tip'

const sessionsStore = useSessionsStore()
const chatStore = useChatStore()
const providers = useProvidersStore()
const ui = useUiStore()
const search = useSearchStore()

const hasMessages = computed(() => sessionsStore.currentMessages.length > 0)

// Temporary chats: New chat offers to start one; a chat that is one says so.
const isTemporary = computed(() => !!sessionsStore.currentSession?.temporary)
const startsTemporary = computed(() => !hasMessages.value && (ui.newChatTemporary || isTemporary.value))
// Side chats: the parent's name, while the parent is still there.
const sideOf = computed(() => (sessionsStore.currentParent ? sessionsStore.currentParent.name : undefined))
const offerTemporary = computed(
  () => !hasMessages.value && !isTemporary.value && !sessionsStore.currentSessionDeprecated &&
    !sessionsStore.currentSession?.shared,
)

// The agent's avatar: only the newest reply's moves.
const newestReply = computed(() => {
  const msgs = sessionsStore.currentMessages
  for (let i = msgs.length - 1; i >= 0; i--) if (msgs[i].role === 'assistant') return i
  return -1
})
// A turn that ends well here gets a happy face; a stop or an error doesn't.
const justFinished = ref(false)
watch(
  () => [sessionsStore.currentSessionId, chatStore.streaming, chatStore.completedTurns] as const,
  ([id, live, done], [wasId, , doneBefore]) => {
    justFinished.value = id === wasId && !live && done > doneBefore
  },
)
const typing = computed(() => !!sessionsStore.currentDraft)

// Deprecated legacy GitHub Copilot sessions are read-only. Show a banner and
// let the user spin up a fresh session.
const isDeprecated = computed(() => sessionsStore.currentSessionDeprecated)

async function startNewSession() {
  try {
    await sessionsStore.createSession({ name: 'New Chat' })
  } catch (e) {
    console.error('Failed to create session:', e)
  }
}

// The assistant is "connected" when ANY provider in the registry is
// authenticated. This used to be a literal `copilot || vertex`, which meant a
// workspace running happily on a config-declared provider was told forever
// that it was not connected — and the Files widget, which gates on
// the same state, stayed hidden.
const anyAssistantConnected = computed(() => providers.anyAuthenticated)
const showOpencodeBanner = computed(
  () => providers.loaded && !anyAssistantConnected.value,
)

// What the banner should say. "Sign in" is the right instruction only when
// there is something to sign in to: for an administrator-provisioned provider
// it is advice the user cannot act on, and it reads as a broken app rather
// than an incomplete setup.
const bannerCopy = computed(() => {
  if (providers.noProvidersConfigured) {
    return {
      title: 'No model provider is configured for this workspace.',
      detail: 'Contact your administrator.',
      action: '',
    }
  }
  if (providers.allManagedAndPending) {
    const names = providers.providers.map(p => p.label).join(', ')
    return {
      title: `Your administrator hasn't finished setting up ${names}.`,
      detail: 'The assistant will work as soon as it is provisioned.',
      action: '',
    }
  }
  return {
    title: "The assistant isn't connected.",
    detail: 'Sign in to use its models.',
    action: 'Connect',
  }
})

// The Files widget only makes sense once signed in (there's
// no usable workspace/model before that). Like the banner, it waits for the
// first provider check to say otherwise, so a signed-in user never waits for it.
const showWidgets = computed(() => !providers.loaded || anyAssistantConnected.value)

// Width of the right-hand column, which holds the Files widget. (The
// `filePanel*` names in the store hold the column's width; the localStorage
// keys are kept as they are so existing preferences survive.)
// The column's header lines up with the chat's header, however tall that is.
const headerRef = ref<InstanceType<typeof ChatHeader> | null>(null)
const headerHeight = ref(0)
const widgetColStyle = computed(() => ({
  '--widget-col-width': ui.filePanelWidth + 'px',
  ...(headerHeight.value ? { '--fp-header-h': headerHeight.value + 'px' } : {}),
}))

// ── Resizing the widget column ──
// The handle lives on the column rather than inside the Files widget, so it
// exists whenever the column does: the column is the thing being resized.
const colResizing = ref(false)
function startColResize(e: PointerEvent) {
  e.preventDefault()
  const startX = e.clientX
  const startW = ui.filePanelWidth
  const handle = e.currentTarget as HTMLElement | null
  try { handle?.setPointerCapture(e.pointerId) } catch { /* not all pointers capture */ }
  colResizing.value = true
  function onMove(ev: PointerEvent) {
    // Column anchored right; dragging left widens it.
    ui.setFilePanelWidth(startW + (startX - ev.clientX))
  }
  function onUp(ev: PointerEvent) {
    try { handle?.releasePointerCapture(ev.pointerId) } catch { /* ditto */ }
    window.removeEventListener('pointermove', onMove)
    window.removeEventListener('pointerup', onUp)
    document.body.style.cursor = ''
    document.body.style.userSelect = ''
    document.body.classList.remove('rw-resizing')
    colResizing.value = false
  }
  window.addEventListener('pointermove', onMove)
  window.addEventListener('pointerup', onUp)
  document.body.style.cursor = 'col-resize'
  document.body.style.userSelect = 'none'
  // Marks the drag globally so the column can drop its width transition — an
  // animated width lags visibly behind the pointer.
  document.body.classList.add('rw-resizing')
}

// ── Following the conversation ──
// The composer floats over the messages rather than sitting in a tray below
// them, so the list scrolls behind it. Two things follow: the list needs
// bottom padding equal to the composer's height, or the last message hides
// underneath it; and we need to know whether the reader is still at the
// bottom, so streaming can stop dragging them there.
const chatMainEl = ref<HTMLElement | null>(null)
const messagesEl = ref<HTMLElement | null>(null)

const atBottom = ref(true)

function onMessagesScroll() {
  const el = messagesEl.value
  if (!el) return
  atBottom.value = isFollowingBottom(el)
}

// Nothing to go back down to on an empty chat.
const showJumpToBottom = computed(() => hasMessages.value && !atBottom.value)

// What the button does. Gliding back is the nicer read when the transcript is
// still — it shows you where you were taken. Mid-stream it is the wrong tool:
// the end keeps moving, so a timed animation lands short of a target that has
// since grown, and you arrive still detached. Then it goes straight there.
function jumpToLatest() {
  scrollToBottom(!chatStore.streaming)
}

// `smooth` is for the button, where the travel shows the reader where they
// were taken. Everything else jumps: animating each streamed chunk would
// leave the view permanently mid-flight.
function scrollToBottom(smooth = false) {
  const el = messagesEl.value
  if (!el) return
  if (smooth) {
    el.scrollTop = el.scrollHeight
  } else {
    const prev = el.style.scrollBehavior
    el.style.scrollBehavior = 'auto'
    el.scrollTop = el.scrollHeight
    el.style.scrollBehavior = prev
  }
  // A scroll event normally settles this, but not when the content is shorter
  // than the viewport (no event fires) or mid-animation.
  atBottom.value = true
}

function jumpToBottom() {
  nextTick(() => scrollToBottom(false))
}
// Following differs from jumping: the decision has to be re-taken inside the
// tick. Chunks land continuously, so one can be queued microseconds before the
// reader scrolls up — and acting on that stale answer would haul them straight
// back, which is exactly the behaviour this is meant to end.
function followBottom() {
  nextTick(() => { if (atBottom.value) scrollToBottom(false) })
}
// Follow the stream only while the reader is already at the end. Yanking
// someone back mid-paragraph is precisely what the jump button exists to
// make unnecessary.
watch(() => chatStore.streamingContent, () => {
  if (chatStore.streaming && atBottom.value) followBottom()
})

// Starting a turn is an explicit act: you want to see the answer to the thing
// you just asked. So sending re-attaches to the end even if you were reading
// back — and if you scroll up again afterwards, the watcher above leaves you
// there.
watch(() => chatStore.streaming, (now, before) => {
  if (now && !before) {
    endSearchFocus()
    atBottom.value = true
    jumpToBottom()
  }
})

// ── Opened from a search result ──
// The chat opens at the result's message rather than the newest one, with the
// words found highlighted throughout. The highlighting lasts until you leave
// the chat or start a turn.
function searchFocusHere() {
  const f = search.focus
  return f && f.sessionId === sessionsStore.currentSessionId ? f : null
}

function endSearchFocus() {
  search.focus = null
  clearHighlights()
}

function showSearchHit() {
  const f = searchFocusHere()
  const el = messagesEl.value
  if (!f || !el || !hasMessages.value) return
  highlightIn(el.querySelectorAll('.msg-bubble'), f.terms)
  if (!f.pending) return
  const target = f.messageId ? el.querySelector<HTMLElement>(`[data-message-id="${f.messageId}"]`) : null
  if (!target) {
    // A loaded history without the message: it's gone, so open as usual.
    if (sessionsStore.currentMessages.some(m => m.id !== undefined)) {
      f.pending = false
      atBottom.value = true
      jumpToBottom()
    }
    return
  }
  f.pending = false
  atBottom.value = false
  target.scrollIntoView({ block: 'center', behavior: 'instant' as ScrollBehavior })
  if (!window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
    const accent = getComputedStyle(document.documentElement).getPropertyValue('--accent').trim()
    ;(target.querySelector('.msg-bubble') || target).animate(
      [{ boxShadow: `0 0 0 3px ${accent}` }, { boxShadow: '0 0 0 3px transparent' }],
      { duration: 1800, easing: 'ease-out' },
    )
  }
}

watch(() => [sessionsStore.currentMessages, search.focus], showSearchHit, { flush: 'post' })

watch(() => sessionsStore.currentSessionId, (id) => {
  if (!id) return
  if (search.focus && search.focus.sessionId !== id) endSearchFocus()
  if (searchFocusHere()?.pending) {
    atBottom.value = false
    return
  }
  // A different conversation, so the previous one's scroll position says
  // nothing about this one. Opening a chat always starts at its newest
  // message. Set synchronously: the messages watcher below fires on the same
  // tick when history arrives, and must see "following" rather than whatever
  // was true of the chat we just left.
  atBottom.value = true
  jumpToBottom()
})

// Fires when history finishes loading and whenever a message is appended.
// Same rule as streaming: only follow if the reader is at the end. Otherwise
// a reply landing while they read back would haul them away from it.
watch(() => sessionsStore.currentMessages, () => {
  if (!chatStore.streaming && atBottom.value) followBottom()
})

// The composer's height moves as the textarea grows, files are attached or
// prompts queue up. Publish it so the padding under the messages and the
// jump button both track it instead of guessing.
let composerObserver: ResizeObserver | null = null
let headerObserver: ResizeObserver | null = null

onMounted(() => {
  if (searchFocusHere()?.pending) atBottom.value = false
  showSearchHit()
  const header = headerRef.value?.$el as HTMLElement | undefined
  if (header) {
    headerObserver = new ResizeObserver(() => { headerHeight.value = header.offsetHeight })
    headerObserver.observe(header)
  }
  const main = chatMainEl.value
  const composer = main?.querySelector<HTMLElement>('#input-area')
  if (!main || !composer) return
  composerObserver = new ResizeObserver(() => {
    main.style.setProperty('--composer-h', `${composer.offsetHeight}px`)
    // A composer that grew would otherwise slide the last message under itself.
    if (atBottom.value) scrollToBottom(false)
  })
  composerObserver.observe(composer)
})

onBeforeUnmount(() => {
  endSearchFocus()
  composerObserver?.disconnect()
  composerObserver = null
  headerObserver?.disconnect()
  headerObserver = null
})
</script>

<template>
  <div class="chat-body">
    <div class="chat-col">
      <ChatHeader ref="headerRef" />
      <div v-if="isDeprecated" class="dep-banner">
        <div class="ab-icon" aria-hidden="true">⚠️</div>
        <div class="ab-msg">
          <strong>This chat is deprecated.</strong>
          <span>It was created with the retired GitHub Copilot backend and is now read-only. Create a new session to keep chatting.</span>
        </div>
        <button class="ab-btn" @click="startNewSession">
          New chat
        </button>
      </div>
      <div v-if="showOpencodeBanner" class="auth-banner">
        <div class="ab-icon" aria-hidden="true">🔒</div>
        <div class="ab-msg">
          <strong>{{ bannerCopy.title }}</strong>
          <span>{{ bannerCopy.detail }}</span>
        </div>
        <button v-if="bannerCopy.action" class="ab-btn" @click="ui.openModal('accounts')">
          {{ bannerCopy.action }}
        </button>
      </div>
      <div class="chat-main" ref="chatMainEl" :class="{ empty: !hasMessages }">
        <button
          v-if="offerTemporary"
          type="button"
          class="temp-toggle"
          :class="{ on: ui.newChatTemporary }"
          :aria-pressed="ui.newChatTemporary"
          v-tip="ui.newChatTemporary ? 'Turn off to save this chat to your history' : 'Start a chat that isn\'t saved. It\'s deleted after 24 hours of inactivity.'"
          @click="ui.newChatTemporary = !ui.newChatTemporary"
        >
          <TimerIcon :size="14" />
          Temporary chat
        </button>
        <div id="messages" ref="messagesEl" @scroll.passive="onMessagesScroll">
          <NewChatGreeting
            v-if="!hasMessages"
            :temporary="startsTemporary"
            :side-of="sideOf"
          />
          <template v-else>
          <p v-if="isTemporary" class="temp-note">
            <TimerIcon :size="13" />
            <template v-if="sideOf !== undefined">
              Side chat of “{{ sideOf || 'Untitled chat' }}”. Nothing here goes back to it, and it's deleted after 24 hours of inactivity.
            </template>
            <template v-else>Temporary chat. It isn't saved and is deleted after 24 hours of inactivity.</template>
          </p>
          <MessageBubble
            v-for="(msg, i) in sessionsStore.currentMessages"
            :key="i"
            :data-message-id="msg.id"
            :role="msg.role"
            :content="msg.content"
            :streaming="i === sessionsStore.currentMessages.length - 1 && chatStore.streaming && msg.role === 'assistant'"
            :step-count="i === sessionsStore.currentMessages.length - 1 && chatStore.streaming && msg.role === 'assistant' ? chatStore.stepCount : 0"
            :tool-calls="i === sessionsStore.currentMessages.length - 1 && chatStore.streaming && msg.role === 'assistant' ? chatStore.toolCalls : (msg.toolCalls || [])"
            :created-at="msg.createdAt"
            :usage="msg.usage"
            :author-label="msg.authorId ? (msg.authorName || 'Guest') : undefined"
            :newest="i === newestReply"
            :finished="i === newestReply && justFinished"
            :typing="i === newestReply && typing"
          />
        <QuestionCard
          v-if="chatStore.currentQuestion"
          :question="chatStore.currentQuestion"
          @answer="chatStore.answerQuestion($event)"
        />
        <PermissionCard
          v-if="chatStore.currentPermission"
          :permission="chatStore.currentPermission"
          @respond="chatStore.respondToPermission($event)"
        />
      </template>
        </div>
        <!-- Only offered once the reader has left the end, so it doesn't sit
             there as a permanent control for something already true. -->
        <Transition name="jump-fade">
          <button
            v-if="showJumpToBottom"
            class="jump-to-bottom"
            @click="jumpToLatest"
            title="Jump to latest message"
            aria-label="Jump to latest message"
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" width="16" height="16" aria-hidden="true">
              <path d="M12 5v14M19 12l-7 7-7-7" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
          </button>
        </Transition>
        <PromptInput />
        <!-- Starter prompts sit *below* the composer: the composer is the thing
             to act on, so it gets the centre line, and the chips read as
             suggestions underneath rather than a queue to get past. -->
        <EmptyState v-if="!hasMessages" />
      </div>
    </div>
    <!-- Drag handle for the widget column. A sibling of the column rather than
         a child, so the column's :empty rule still decides whether the column
         shows at all; the handle hides with it. -->
    <div
      v-if="showWidgets"
      class="rw-resize"
      :class="{ dragging: colResizing }"
      role="separator"
      aria-orientation="vertical"
      title="Drag to resize"
      @pointerdown="startColResize"
    ></div>
    <!-- Right-hand column for the Files widget, the full height of the page
         with its header in line with the chat's. It's a real flex sibling of
         the chat (not an overlay), so the chat reflows to its left. -->
    <div v-if="showWidgets" class="right-widgets" :class="{ 'mobile-open': ui.mobileFilesOpen }" :style="widgetColStyle">
      <div v-if="ui.mobileFilesOpen" class="mobile-files-bar">
        <strong>Files</strong>
        <button class="mfb-close" @click="ui.closeMobileFiles()" aria-label="Close files" title="Close">✕</button>
      </div>
      <WorkspaceFilePanel />
    </div>
  </div>
</template>

<style scoped>
/* Right-hand column for the Files widget: the right side of the page, top to
   bottom, so the widget's header sits in the bar the chat's header starts.
   The column width follows the Files widget width (resizable). */
.right-widgets {
  flex-shrink: 0;
  width: var(--widget-col-width, 320px);
  max-width: 96vw;
  display: flex;
  flex-direction: column;
  min-height: 0;
  overflow-y: auto;
  align-self: stretch;
  border-left: 1px solid var(--border);
  background: var(--surface);
  /* Opening a file widens this column on its own; easing it makes the chat
     visibly give way rather than the layout jumping under the reader. */
  transition: width .2s ease;
}
/* The Files widget decides its own visibility (it waits for a non-empty
   workspace), and when it doesn't render, this column would still hold its
   width as a blank margin. Collapsing it here keeps that decision with the
   widget. (v-if leaves comment nodes behind, which :empty ignores.) */
.right-widgets:empty {
  display: none;
}
/* A drag already tracks the pointer exactly, so animating it on top only
   makes the panel edge lag behind the cursor. */
body.rw-resizing .right-widgets { transition: none; }

/* Drag handle for the column. Laid out as a zero-width flex item (10px basis
   pulled back by an equal negative margin) and then shifted left, so it
   straddles the column's edge without taking any width of its own — the chat
   keeps exactly the space it had. */
.rw-resize {
  flex: 0 0 10px;
  margin-right: -10px;
  position: relative;
  left: -5px;
  cursor: col-resize;
  z-index: 3;
  touch-action: none;
}
/* The grab area is the full 10px, but only a hairline lights up: this handle
   runs the whole height of the chat, and filling it would put a solid bar
   down the middle of the window every time the pointer crossed the gutter. */
.rw-resize::after {
  content: '';
  position: absolute;
  top: 0; bottom: 0; left: 4px;
  width: 2px;
  border-radius: 2px;
  background: transparent;
  transition: background .15s;
}
.rw-resize:hover::after, .rw-resize.dragging::after { background: var(--accent); }
/* The column hides itself when neither widget renders (see :empty above).
   Without this the handle would be left behind, resizing nothing. */
.chat-body:has(> .right-widgets:empty) > .rw-resize { display: none; }
@media (max-width: 900px) {
  /* The column is hidden or full-screen at this width; neither state wants a
     drag handle. */
  .rw-resize { display: none; }
  /* On narrow screens the widget column would crowd the chat — hide it. */
  .right-widgets { display: none; }
  /* …unless opened from the header Files button, in which case show it as a
     full-screen overlay so the file viewer (and its markdown preview) gets
     the whole screen. */
  .right-widgets.mobile-open {
    display: flex;
    position: fixed;
    inset: 0;
    width: 100%;
    max-width: 100%;
    z-index: 60;
    background: var(--bg, var(--surface));
    border-left: none;
    padding: 8px;
    overflow: auto;
  }
  .right-widgets.mobile-open .mobile-files-bar {
    display: flex; align-items: center; justify-content: space-between;
    padding: 2px 4px 8px; flex-shrink: 0;
  }
  .mfb-close {
    background: none; border: 1px solid var(--border); border-radius: 8px;
    width: 32px; height: 32px; color: var(--text2); cursor: pointer;
  }
  .mfb-close:hover { background: var(--surface2); color: var(--text); }
}
/* The close bar belongs to the mobile overlay only (e.g. if the viewport is
   widened while the overlay flag is still set). */
.mobile-files-bar { display: none; }
.chat-body {
  flex: 1;
  display: flex;
  flex-direction: row;
  min-height: 0;
  overflow: hidden;
  position: relative;
}
/* The chat's own column: its header, banners and the conversation. */
.chat-col {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
}
.chat-main {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-width: 0;
  min-height: 0;
  overflow: hidden;
  position: relative;
  /* Height of the strip above the composer over which the conversation fades
     out. Named because both the composer's top padding and the jump button's
     offset are measured from it. */
  --composer-fade: 32px;
}
/* Once there is a conversation, the composer floats over it instead of
   sitting in a tray beneath it: the text runs right up to the composer and
   passes behind it. `--composer-h` is measured in script, because the
   composer grows with the textarea, attachments and queued prompts. */
.chat-main:not(.empty) :deep(#input-area) {
  position: absolute;
  left: 0; right: 0; bottom: 0;
  /* Fades the conversation out as it slides under, rather than cutting it
     against a hard edge. */
  background: linear-gradient(to bottom, transparent, var(--bg) var(--composer-fade));
  padding-top: var(--composer-fade);
  /* The fade strip spans the full width, so let scrolls and clicks aimed at
     the conversation pass through it; the composer's own rows take them back. */
  pointer-events: none;
}
.chat-main:not(.empty) :deep(#input-area) > * { pointer-events: auto; }
.chat-main:not(.empty) #messages {
  /* Room to scroll the last message clear of the floating composer. */
  padding-bottom: calc(var(--composer-h, 132px) + 12px);
}
/* Empty (new-chat) state: greeting, composer and starter prompts sit as one
   group straddling the horizontal mid-line of the chat area.

   The group is placed by giving the greeting row a fixed share of the height,
   which pins the composer's top edge to a point that doesn't move: the
   composer can't drift as the greeting wraps, the chip row grows or the
   textarea grows with typing. Centring the stack as one group instead does
   move it, which is why it isn't done that way.

   The 81px is what makes the *group* centred rather than just that pin.
   Measured from the composer's top edge, the group hangs ~233px below it
   (composer ~145px + chips ~88px) but only ~70px above (the greeting and its
   gap). Landing that edge exactly on the mid-line therefore left the whole
   block 69px low — enough to read as awkwardly bottom-heavy, with 340px of
   space above it and 202px below on a 900px-tall window. Half the difference,
   (233 - 70) / 2 ≈ 81px, is how far above the mid-line the edge has to sit for
   the group to balance around it. A constant, not a percentage: the group's
   height barely changes with the viewport, so the correction it needs doesn't
   either — this holds the balance at every window size rather than at one.

   max() keeps the greeting its own room on very short windows, where half the
   height is less than the headline needs and the subtraction would otherwise
   push it up under the header. */
.chat-main.empty {
  justify-content: flex-start;
}
.temp-toggle {
  position: absolute; top: 14px; right: 16px; z-index: 2;
  display: inline-flex; align-items: center; gap: 6px;
  padding: 6px 12px; border-radius: 8px;
  border: 1px solid var(--border); background: var(--surface);
  color: var(--text2); font: inherit; font-size: 13px; font-weight: 500; cursor: pointer;
  transition: background .12s, color .12s, border-color .12s;
}
.temp-toggle:hover { background: var(--hover); color: var(--text); }
.temp-toggle.on {
  border-style: dashed; border-color: var(--accent);
  background: color-mix(in srgb, var(--accent) 10%, var(--surface)); color: var(--text);
}
.temp-toggle.on :deep(svg) { color: var(--accent); }
.temp-note {
  display: flex; align-items: center; justify-content: center; gap: 6px;
  max-width: 860px; margin: 4px auto 14px; padding: 0 16px;
  font-size: 12px; color: var(--text2); text-align: center;
}
@media (max-width: 768px) {
  .temp-toggle { top: 10px; right: 10px; }
}
.chat-main.empty #messages {
  flex: 0 0 max(96px, calc(50% - 81px));
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: flex-end;
  overflow: visible;
  /* Inside the half, so it opens up the greeting without moving the composer. */
  padding: 20px 0 32px;
}
/* A short window can leave the chips no room; let them scroll rather than
   shove the composer off the line it was placed on. */
.chat-main.empty :deep(.empty-state) {
  flex: 0 1 auto;
  min-height: 0;
  overflow-y: auto;
}
/* The saved-prompt editor is taller than the lower half, so holding the
   composer on the mid-line would strand its Save button below the fold. While
   that form is open the greeting gives up its fixed half and the whole stack
   rises, handing the editor the rest of the column. The centred layout comes
   back when the form closes. */
.chat-main.empty:has(.sp-form) #messages {
  flex-basis: auto;
}

/* Sits just above the composer's visible box, centred on the conversation.
   The measured height includes the fade strip, which is see-through, so that
   comes back off or the button floats away from what it belongs to. */
.jump-to-bottom {
  position: absolute;
  left: 50%;
  transform: translateX(-50%);
  bottom: calc(var(--composer-h, 132px) - var(--composer-fade) + 10px);
  z-index: 5;
  width: 34px; height: 34px;
  display: flex; align-items: center; justify-content: center;
  border-radius: 999px;
  border: 1px solid var(--border);
  background: var(--surface);
  color: var(--text2);
  cursor: pointer;
  box-shadow: 0 2px 10px rgba(0,0,0,.18);
  transition: color .15s, border-color .15s, background .15s;
}
.jump-to-bottom:hover {
  color: var(--accent);
  border-color: var(--accent);
  background: var(--surface2);
}
.jump-fade-enter-active,
.jump-fade-leave-active { transition: opacity .15s ease; }
.jump-fade-enter-from,
.jump-fade-leave-to { opacity: 0; }
#messages {
  flex: 1;
  overflow-y: auto;
  padding: 20px 0 8px;
  scroll-behavior: smooth;
  scrollbar-color: var(--scrollbar-thumb) var(--scrollbar-track);
  scrollbar-width: thin;
}
#messages::-webkit-scrollbar { width: 10px; }
#messages::-webkit-scrollbar-track { background: var(--scrollbar-track); }
#messages::-webkit-scrollbar-thumb {
  background: var(--scrollbar-thumb);
  border: 2px solid transparent;
  border-radius: 999px;
  background-clip: content-box;
}
#messages::-webkit-scrollbar-thumb:hover { background: var(--scrollbar-thumb-h); background-clip: content-box; }

.auth-banner {
  display: flex; align-items: center; gap: 12px;
  padding: 10px 16px;
  background: rgba(255, 193, 7, 0.10);
  border-bottom: 1px solid rgba(255, 193, 7, 0.35);
  color: var(--text);
}
.dep-banner {
  display: flex; align-items: center; gap: 12px;
  padding: 10px 16px;
  background: rgba(248, 81, 73, 0.10);
  border-bottom: 1px solid rgba(248, 81, 73, 0.35);
  color: var(--text);
}
.ab-icon { font-size: 18px; }
.ab-msg { display: flex; flex-direction: column; gap: 2px; flex: 1; min-width: 0; }
.ab-msg strong { font-size: 13px; }
.ab-msg span { font-size: 12px; color: var(--text2); }
.ab-btn {
  padding: 7px 14px;
  background: var(--accent, #1f6feb); color: #fff;
  border: none; border-radius: 6px;
  font-size: 13px; font-weight: 600;
  cursor: pointer;
  transition: background .15s;
}
.ab-btn:hover { background: var(--accent-hover, #388bfd); }
@media (max-width: 768px) {
  .auth-banner { flex-wrap: wrap; }
  .dep-banner { flex-wrap: wrap; }
  .ab-btn { width: 100%; }
}

@media (max-width: 768px) {
  #messages { padding: 12px 0 6px; }
}
</style>
