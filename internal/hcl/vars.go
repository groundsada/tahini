package hcl

import (
	"regexp"
	"sort"
)

var varBlock = regexp.MustCompile(`(?m)^\s*variable\s+"([^"]+)"`)

// ParseVariables extracts variable names declared in HCL content.
func ParseVariables(hcl string) []string {
	matches := varBlock.FindAllStringSubmatch(hcl, -1)
	seen := map[string]bool{}
	var out []string
	for _, m := range matches {
		name := m[1]
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
