// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package check

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/yuin/goldmark/ast"

	"github.com/strong-network/Knowledge-Worker-Agent/internal/fileshare/markdown"
)

// Markdown checks a Markdown document on the parse viewers see it from. A
// picture is the only load; HTML written into it isn't shown.
func Markdown(src []byte) Result {
	c := &collector{res: Result{Findings: []Finding{}}, index: map[string]int{}}
	var newlines []int
	for i, b := range src {
		if b == '\n' {
			newlines = append(newlines, i)
		}
	}
	lineOf := func(n ast.Node) int {
		for ; n != nil; n = n.Parent() {
			if p := n.Pos(); p >= 0 {
				return 1 + sort.SearchInts(newlines, p)
			}
		}
		return 1
	}
	_ = ast.Walk(markdown.Parse(src), func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *ast.Image:
			dest := n.Destination
			c.load(picture, string(dest), lineOf(n))
			if len(dest) >= 5 && strings.EqualFold(string(dest[:5]), "data:") && !markdown.Shown(dest) {
				c.add(false, "Has a picture in a format that isn't shown: only PNG, JPEG, GIF and WebP are", lineOf(n))
			}
		case *ast.Link:
			if where, ok := isFileLink(string(n.Destination)); ok {
				c.add(false, fmt.Sprintf("Links to another file, %s, which won't open", where), lineOf(n))
			}
		case *ast.HTMLBlock:
			if n.HTMLBlockType != ast.HTMLBlockType2 {
				c.add(false, "Has HTML written into it, which isn't shown", lineOf(n))
			}
		case *ast.RawHTML:
			if n.Segments.Len() == 0 {
				break
			}
			if seg := n.Segments.At(0); !bytes.HasPrefix(seg.Value(src), []byte("<!--")) {
				c.add(false, "Has HTML written into it, which isn't shown", lineOf(n))
			}
		}
		return ast.WalkContinue, nil
	})
	return c.res
}
