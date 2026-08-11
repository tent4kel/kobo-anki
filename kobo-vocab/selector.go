package main

import (
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

// A deliberately small CSS subset, enough to describe where a dictionary
// keeps its translations and examples and no more: tag names, ".class",
// descendant and ">" child combinators, and ":nth-of-type(n)".
//
// The point is that a dictionary's *structure* should be data in a config
// file rather than branches in this binary. Every family examined so far -
// this project's own Van Dale build, a StarDict conversion, and WikDict's
// generated dicts - is expressible in this subset. Anything that needs more
// than this is better served by fixing the dictionary than by growing the
// selector language.
type selStep struct {
	tag    string // "" or "*" = any element
	class  string // "" = any
	nth    int    // 1-based nth-of-type; 0 = any position
	direct bool   // reached via ">" rather than by descent
}

type selector []selStep

// parseSelector is deliberately total: anything it cannot interpret becomes
// a step that simply matches nothing, so a typo in a config file yields an
// empty result (which the caller reports as low coverage) rather than a
// parse error on a device with no way to show one.
func parseSelector(s string) selector {
	var sel selector
	direct := false
	for _, tok := range strings.Fields(s) {
		if tok == ">" {
			direct = true
			continue
		}
		st := selStep{direct: direct}
		direct = false
		if i := strings.Index(tok, ":nth-of-type("); i >= 0 {
			n, err := strconv.Atoi(strings.TrimSuffix(tok[i+len(":nth-of-type("):], ")"))
			if err == nil {
				st.nth = n
			}
			tok = tok[:i]
		}
		if i := strings.Index(tok, "."); i >= 0 {
			st.class = tok[i+1:]
			tok = tok[:i]
		}
		st.tag = tok
		sel = append(sel, st)
	}
	return sel
}

func (st selStep) matches(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	if st.tag != "" && st.tag != "*" && n.Data != st.tag {
		return false
	}
	if st.class != "" && !hasClass(n, st.class) {
		return false
	}
	return true
}

func hasClass(n *html.Node, want string) bool {
	for _, a := range n.Attr {
		if a.Key != "class" {
			continue
		}
		for _, c := range strings.Fields(a.Val) {
			if c == want {
				return true
			}
		}
	}
	return false
}

// typeIndex is a node's 1-based position among the siblings sharing its tag,
// which is what :nth-of-type counts.
func typeIndex(n *html.Node) int {
	if n.Parent == nil {
		return 0
	}
	i := 0
	for s := n.Parent.FirstChild; s != nil; s = s.NextSibling {
		if s.Type == html.ElementNode && s.Data == n.Data {
			i++
			if s == n {
				return i
			}
		}
	}
	return 0
}

// query returns every node under root matching sel, in document order.
func query(root *html.Node, sel selector) []*html.Node {
	if len(sel) == 0 {
		return nil
	}
	cur := []*html.Node{root}
	for _, st := range sel {
		var next []*html.Node
		for _, base := range cur {
			collect(base, st, &next)
		}
		if st.nth > 0 {
			kept := next[:0]
			for _, n := range next {
				if typeIndex(n) == st.nth {
					kept = append(kept, n)
				}
			}
			next = kept
		}
		if len(next) == 0 {
			return nil
		}
		cur = next
	}
	return cur
}

// queryFirst is query for the common "I only want one of these" case.
func queryFirst(root *html.Node, sel selector) *html.Node {
	if ns := query(root, sel); len(ns) > 0 {
		return ns[0]
	}
	return nil
}

func collect(n *html.Node, st selStep, out *[]*html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if st.matches(c) {
			*out = append(*out, c)
		}
		if !st.direct {
			collect(c, st, out)
		}
	}
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// removeMatching deletes every node matching sel from the tree. Used for
// annotations the user asked not to see (dict_strip_labels) and for the
// custom elements some dictionaries use as inline subject markers.
func removeMatching(root *html.Node, sel selector) {
	for _, n := range query(root, sel) {
		if n.Parent != nil {
			n.Parent.RemoveChild(n)
		}
	}
}
