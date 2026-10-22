// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package viewer

import (
	"bytes"

	"golang.org/x/net/html"
)

// linkScript makes a link to a web page open in a new tab, as the sandbox
// allows: followed inside the frame, the viewer page's policy refuses it.
// Links within the file, downloads and script links are left as they are.
const linkScript = `<script>addEventListener("click",function(e){` +
	`var a=e.target instanceof Element?e.target.closest("a,area"):null;` +
	`if(!a||a.hasAttribute("download"))return;` +
	`var u;try{u=new URL(a.getAttribute("href")||a.getAttribute("xlink:href")||"",document.baseURI)}catch(_){return}` +
	`if(!/^https?:$/.test(u.protocol)||u.href.split("#")[0]===location.href.split("#")[0])return;` +
	`a.setAttribute("target","_blank")},true)</script>`

var utf8BOM = []byte("\xef\xbb\xbf")

// withLinkScript puts linkScript first in an HTML file, so it runs before the
// file's own scripts, and after its doctype, so the file keeps its rendering mode.
func withLinkScript(src []byte) []byte {
	at := 0
	if bytes.HasPrefix(src, utf8BOM) {
		at = len(utf8BOM)
	}
	z := html.NewTokenizer(bytes.NewReader(src[at:]))
	skipped := 0
scan:
	for {
		switch z.Next() {
		case html.CommentToken:
			skipped += len(z.Raw())
		case html.TextToken:
			if len(bytes.TrimSpace(z.Raw())) > 0 {
				break scan
			}
			skipped += len(z.Raw())
		case html.DoctypeToken:
			at += skipped + len(z.Raw())
			break scan
		default:
			break scan
		}
	}
	out := make([]byte, 0, len(src)+len(linkScript))
	out = append(out, src[:at]...)
	out = append(out, linkScript...)
	return append(out, src[at:]...)
}
