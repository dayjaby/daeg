package parser

import "strings"

// SubstituteArgs replaces ARG references in src with their values.
// ARG declarations are collected from the file first, then any
// ${VAR} or $VAR occurrences in subsequent lines are substituted.
// This must run before Parse() so that MERGE(${STAGE_A}, ${STAGE_B})
// resolves to real stage names before the parser sees them.
func SubstituteArgs(src string, overrides map[string]string) string {
	// First pass: collect all ARG declarations and their defaults.
	args := make(map[string]string)
	lines := strings.Split(src, "\n")

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		upper := strings.ToUpper(trimmed)
		if !strings.HasPrefix(upper, "ARG ") {
			continue
		}
		rest := strings.TrimSpace(trimmed[4:])
		key, value, hasDefault := strings.Cut(rest, "=")
		key = strings.TrimSpace(key)
		if hasDefault {
			// Only set if not already overridden by a prior declaration with a value.
			if _, seen := args[key]; !seen {
				args[key] = strings.Trim(strings.TrimSpace(value), `"`)
			}
		} else if _, seen := args[key]; !seen {
			args[key] = "" // declared but no default, and not yet seen
		}
		// Inner ARG re-declarations without a default (Dockerfile scoping pattern)
		// do not overwrite the top-level default collected earlier.
	}

	// Overrides from --build-arg flags win over defaults.
	for k, v := range overrides {
		args[k] = v
	}

	// Second pass: substitute all ${VAR} and $VAR occurrences.
	// We do a simple iterative substitution — not a full shell expander,
	// but correct for the common cases we need.
	var out []string
	for _, line := range lines {
		out = append(out, substitute(line, args))
	}
	return strings.Join(out, "\n")
}

// substitute replaces ${VAR} and $VAR in a single line.
func substitute(s string, args map[string]string) string {
	// Handle ${VAR} form first.
	for k, v := range args {
		s = strings.ReplaceAll(s, "${"+k+"}", v)
		s = strings.ReplaceAll(s, "$"+k, v)
	}
	return s
}
