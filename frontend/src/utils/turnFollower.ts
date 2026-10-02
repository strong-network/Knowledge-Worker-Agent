// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Releases the frames of an /events connection in order, deciding for each turn
// whether this tab should show it. Shared by the owner's app and the
// guest page, which differ only in the hooks.
//
// /events sends `ready`, then every turn from that moment: a `turn_start`
// naming it, its frames, and usually `done`. Two things make a naive reader
// wrong. A turn can finish between the tab loading history and the
// connection's `ready`, reaching neither — so history is reloaded on `ready`
// and every frame waits until that settles. And a turn can already be on
// screen — in that history, or on the tab's own stream — so `decide` names each
// turn as one to render, to skip, or to hold until the tab is free.

export interface TurnStart {
  turn_id?: string
  prompt?: string
  author_id?: string | null
  author_name?: string | null
}

export type TurnDecision = 'render' | 'skip' | 'wait'

export interface TurnFollowerHooks {
  // Reload the transcript after `ready`. Resolving to false means it could not
  // be applied yet: `ready` is held and retried when canSync next allows.
  sync(): Promise<unknown>
  // Whether a reload may start now; the owner's tab holds it while its own
  // stream has not yet said which turn it carries.
  canSync?(): boolean
  decide(turn: TurnStart): TurnDecision
  begin(turn: TurnStart): void
  frame(event: string, data: any): void
  // The rendered turn is over: with its `done` frame, or cut off — replaced
  // by the next turn without one, or its connection gone.
  end(completed: boolean): void
  // The server ended the connection for good (sharing ended, chat deleted).
  closed?(reason: string): void
}

const FINAL = new Set(['sharing_ended', 'session_deleted'])

export class TurnFollower {
  private frames: Array<[string, any]> = []
  private synced = false
  private syncing = false
  private mode: 'idle' | 'skip' | 'render' = 'idle'
  private generation = 0
  private pumping = false
  private again = false

  constructor(private hooks: TurnFollowerHooks) {}

  /** True once history has been reloaded since `ready`: every turn from here reaches the tab. */
  get live(): boolean {
    return this.synced
  }

  push(event: string, data: any) {
    if (FINAL.has(event)) {
      this.reset()
      this.hooks.closed?.(event)
      return
    }
    this.frames.push([event, data])
    this.pump()
  }

  /** Forget the connection: frames not yet released, and a turn left half-shown. */
  reset() {
    this.generation++
    this.frames = []
    this.synced = false
    this.syncing = false
    if (this.mode === 'render') {
      this.mode = 'idle'
      this.hooks.end(false)
    }
    this.mode = 'idle'
  }

  /** Release what can be released. Call again whenever a hook's answer may have changed. */
  pump() {
    if (this.pumping) {
      this.again = true
      return
    }
    this.pumping = true
    try {
      do {
        this.again = false
        this.drain()
      } while (this.again)
    } finally {
      this.pumping = false
    }
  }

  private drain() {
    while (this.frames.length) {
      const [event, data] = this.frames[0]
      if (event === 'ready') {
        if (this.syncing || (this.hooks.canSync && !this.hooks.canSync())) return
        this.frames.shift()
        this.syncing = true
        const generation = this.generation
        this.hooks.sync().catch(() => {}).then((applied) => {
          if (generation !== this.generation) return
          this.syncing = false
          if (applied === false) this.frames.unshift([event, data])
          else this.synced = true
          this.pump()
        })
        return
      }
      if (!this.synced) return
      if (event === 'turn_start') {
        if (this.mode === 'render') {
          this.mode = 'idle'
          this.hooks.end(false)
        }
        const decision = this.hooks.decide(data || {})
        if (decision === 'wait') return
        this.frames.shift()
        this.mode = decision
        if (decision === 'render') this.hooks.begin(data || {})
        continue
      }
      this.frames.shift()
      if (this.mode !== 'render') continue
      this.hooks.frame(event, data)
      if (event === 'done') {
        this.mode = 'idle'
        this.hooks.end(true)
      }
    }
  }
}
