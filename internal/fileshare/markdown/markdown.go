// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

// Package markdown reads a shared Markdown file the one way both the check
// and the viewer use: the GitHub flavour, without the HTML written into it.
package markdown

import (
	"bytes"
	"io"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Not html.WithUnsafe: raw HTML is left out and script addresses dropped, so
// the rendered page needs no sanitizer.
var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithParserOptions(
		parser.WithAutoHeadingID(),
		parser.WithASTTransformers(util.Prioritized(newTab{}, 100)),
	),
)

// Parse returns the document's tree. Its nodes point into src.
func Parse(src []byte) ast.Node {
	return md.Parser().Parse(text.NewReader(src))
}

// Render writes the document as HTML.
func Render(w io.Writer, src []byte) error {
	return md.Renderer().Render(w, src, Parse(src))
}

// Shown reports whether a link's or a picture's address is kept when the
// document is rendered. Script and file addresses aren't, nor are data: ones
// other than PNG, JPEG, GIF and WebP pictures.
func Shown(dest []byte) bool {
	return !html.IsDangerousURL(dest)
}

// newTab opens links in a new tab, except those to a place in the page: the
// document is framed in its viewer page, which must stay put.
type newTab struct{}

func (newTab) Transform(doc *ast.Document, _ text.Reader, _ parser.Context) {
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch l := n.(type) {
		case *ast.Link:
			if bytes.HasPrefix(l.Destination, []byte("#")) {
				return ast.WalkContinue, nil
			}
		case *ast.AutoLink:
		default:
			return ast.WalkContinue, nil
		}
		n.SetAttributeString("target", "_blank")
		n.SetAttributeString("rel", "noopener noreferrer")
		return ast.WalkContinue, nil
	})
}
