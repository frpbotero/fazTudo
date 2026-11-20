package health

import (
	"encoding/json"
	"os"
	"slices"
)

// codeGraph is the part of graphify's node-link graph.json that places
// symbols in files and links them.
type codeGraph struct {
	Nodes []struct {
		ID         string `json:"id"`
		SourceFile string `json:"source_file"`
	} `json:"nodes"`
	Links []struct {
		Source   string `json:"source"`
		Target   string `json:"target"`
		Relation string `json:"relation"`
	} `json:"links"`
}

// dependencyRelations are the links that make one file depend on another.
var dependencyRelations = map[string]bool{"calls": true, "references": true, "imports_from": true, "embeds": true}

// readCycles returns, for each area in a dependency cycle, the other areas of
// that cycle. areaOf maps a file to its area ("" = none).
func readCycles(graphPath string, areaOf func(string) string) (map[string][]string, error) {
	data, err := os.ReadFile(graphPath)
	if err != nil {
		return nil, err
	}
	var g codeGraph
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, err
	}
	fileOf := make(map[string]string, len(g.Nodes))
	for _, n := range g.Nodes {
		fileOf[n.ID] = n.SourceFile
	}
	edges := map[string][]string{}
	for _, l := range g.Links {
		if !dependencyRelations[l.Relation] {
			continue
		}
		from, to := areaOf(fileOf[l.Source]), areaOf(fileOf[l.Target])
		if from == "" || to == "" || from == to || slices.Contains(edges[from], to) {
			continue
		}
		edges[from] = append(edges[from], to)
	}
	out := map[string][]string{}
	for _, scc := range stronglyConnected(edges) {
		if len(scc) < 2 {
			continue
		}
		slices.Sort(scc)
		for _, a := range scc {
			out[a] = slices.DeleteFunc(slices.Clone(scc), func(b string) bool { return b == a })
		}
	}
	return out, nil
}

// stronglyConnected is Tarjan's algorithm over a small graph of areas.
func stronglyConnected(edges map[string][]string) [][]string {
	var nodes []string
	for n, tos := range edges {
		nodes = append(nodes, n)
		nodes = append(nodes, tos...)
	}
	slices.Sort(nodes)
	nodes = slices.Compact(nodes)

	index, low := map[string]int{}, map[string]int{}
	onStack := map[string]bool{}
	var stack []string
	var out [][]string
	next := 0
	var visit func(string)
	visit = func(v string) {
		index[v], low[v] = next, next
		next++
		stack = append(stack, v)
		onStack[v] = true
		for _, w := range edges[v] {
			if _, seen := index[w]; !seen {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if onStack[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] != index[v] {
			return
		}
		var scc []string
		for {
			w := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			onStack[w] = false
			scc = append(scc, w)
			if w == v {
				break
			}
		}
		out = append(out, scc)
	}
	for _, n := range nodes {
		if _, seen := index[n]; !seen {
			visit(n)
		}
	}
	return out
}
