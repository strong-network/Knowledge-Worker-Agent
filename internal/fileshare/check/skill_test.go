// Copyright 2026 Citrix Systems, Inc.
// SPDX-License-Identifier: Apache-2.0

package check

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The reports-and-dashboards skill tells the agent what a shared file can't
// do. These tests keep it in step with the check: a new rule here fails them
// until the skill names it too, in this repository and in any copy that
// central configuration assigns.
var skillPath = filepath.Join("..", "..", "defaults", "skills", "reports-and-dashboards", "SKILL.md")

func skillText(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// checkedElements are the elements the check reads, from its switches on a
// token's name.
func checkedElements(t *testing.T) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), "check.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		sw, ok := n.(*ast.SwitchStmt)
		if !ok {
			return true
		}
		if sel, ok := sw.Tag.(*ast.SelectorExpr); !ok || sel.Sel.Name != "Data" {
			return true
		}
		for _, stmt := range sw.Body.List {
			for _, e := range stmt.(*ast.CaseClause).List {
				if lit, ok := e.(*ast.BasicLit); ok {
					s, _ := strconv.Unquote(lit.Value)
					out = append(out, s)
				}
			}
		}
		return true
	})
	if len(out) < 10 {
		t.Fatalf("found only %v: has the check changed shape?", out)
	}
	return out
}

func TestTheSkillNamesEveryElementTheCheckReads(t *testing.T) {
	text := skillText(t)
	for _, el := range checkedElements(t) {
		if !regexp.MustCompile(`<` + regexp.QuoteMeta(el) + `\b`).MatchString(text) {
			t.Errorf("the skill doesn't name <%s>, which the check reads", el)
		}
	}
}

func TestTheSkillNamesEveryAPITheCheckFlags(t *testing.T) {
	text := skillText(t)
	var names []string
	group := regexp.MustCompile(`\((?:\?:)?([A-Za-z]+(?:\|[A-Za-z]+)+)\)`)
	for _, re := range append(append([]*regexp.Regexp{}, connects...), storage) {
		for _, m := range group.FindAllStringSubmatch(re.String(), -1) {
			names = append(names, strings.Split(m[1], "|")...)
		}
	}
	if len(names) < 8 {
		t.Fatalf("found only %v: have the patterns changed shape?", names)
	}
	// What the check finds without a name in a pattern: XMLHttpRequest by its
	// open(), the attributes and CSS it reads, and the pictures Markdown shows.
	names = append(names, "XMLHttpRequest", "srcset", "poster", "background", "url(", "@import", "PNG", "JPEG", "GIF", "WebP")
	for _, name := range names {
		if !strings.Contains(text, name) {
			t.Errorf("the skill doesn't name %s, which the check flags", name)
		}
	}
}
