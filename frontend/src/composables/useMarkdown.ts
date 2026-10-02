// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

import { marked } from 'marked'
import DOMPurify from 'dompurify'
import hljs from 'highlight.js/lib/core'
import javascript from 'highlight.js/lib/languages/javascript'
import typescript from 'highlight.js/lib/languages/typescript'
import python from 'highlight.js/lib/languages/python'
import bash from 'highlight.js/lib/languages/bash'
import json from 'highlight.js/lib/languages/json'
import yaml from 'highlight.js/lib/languages/yaml'
import css from 'highlight.js/lib/languages/css'
import xml from 'highlight.js/lib/languages/xml'
import go from 'highlight.js/lib/languages/go'
import sql from 'highlight.js/lib/languages/sql'
import markdown from 'highlight.js/lib/languages/markdown'
import dockerfile from 'highlight.js/lib/languages/dockerfile'
import diff from 'highlight.js/lib/languages/diff'

// Register languages
hljs.registerLanguage('javascript', javascript)
hljs.registerLanguage('js', javascript)
hljs.registerLanguage('typescript', typescript)
hljs.registerLanguage('ts', typescript)
hljs.registerLanguage('python', python)
hljs.registerLanguage('bash', bash)
hljs.registerLanguage('sh', bash)
hljs.registerLanguage('shell', bash)
hljs.registerLanguage('json', json)
hljs.registerLanguage('yaml', yaml)
hljs.registerLanguage('yml', yaml)
hljs.registerLanguage('css', css)
hljs.registerLanguage('html', xml)
hljs.registerLanguage('xml', xml)
hljs.registerLanguage('go', go)
hljs.registerLanguage('golang', go)
hljs.registerLanguage('sql', sql)
hljs.registerLanguage('markdown', markdown)
hljs.registerLanguage('md', markdown)
hljs.registerLanguage('dockerfile', dockerfile)
hljs.registerLanguage('diff', diff)

// Configure marked
marked.setOptions({
  breaks: true,
  gfm: true,
})

// Force every rendered link to open in a new tab. Runs after DOMPurify has
// sanitized attributes so it also applies to links whose markup we didn't
// author. rel="noopener noreferrer" prevents the opened tab from accessing
// window.opener and avoids referrer leakage.
DOMPurify.addHook('afterSanitizeAttributes', (node) => {
  if (node.tagName === 'A') {
    node.setAttribute('target', '_blank')
    node.setAttribute('rel', 'noopener noreferrer')
  }
})

export function renderMarkdown(text: string): string {
  if (!text) return ''
  const raw = marked.parse(text) as string
  // A form in an answer, or in text it quotes, could send what the user types to another site.
  return DOMPurify.sanitize(raw, { FORBID_TAGS: ['form'] })
}

export function highlightCode(el: HTMLElement) {
  el.querySelectorAll('pre code').forEach((block) => {
    const codeEl = block as HTMLElement
    // Try to detect language from class
    const langClass = Array.from(codeEl.classList).find(c => c.startsWith('language-'))
    const lang = langClass?.replace('language-', '') || ''

    if (lang && hljs.getLanguage(lang)) {
      hljs.highlightElement(codeEl)
    } else {
      hljs.highlightElement(codeEl)
    }

    // Wrap in header + code block structure
    const pre = codeEl.parentElement
    if (pre && !pre.querySelector('.pre-header')) {
      const header = document.createElement('div')
      header.className = 'pre-header'
      header.innerHTML = `
        <span class="pre-lang">${lang || 'code'}</span>
        <button class="copy-btn" onclick="navigator.clipboard.writeText(this.closest('pre').querySelector('code').textContent).then(()=>{this.textContent='Copied!';setTimeout(()=>this.textContent='Copy',1500)})">Copy</button>
      `
      pre.insertBefore(header, pre.firstChild)
    }
  })
}
