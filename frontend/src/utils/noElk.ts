// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Stands in for elkjs, which mermaid loads for its ELK layouts. elkjs is under
// the EPL-2.0, so the build leaves it out (vite.config.ts) and diagrams use
// the dagre layout instead (replyVisuals.ts). A diagram that asks for ELK
// itself shows its source and this message.
export default class ELK {
  constructor() {
    throw new Error('the ELK layout is not available here; leave out the layout setting')
  }
}
