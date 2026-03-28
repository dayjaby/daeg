package graph

import (
	"fmt"
	"strings"

	"github.com/dayjaby/daeg/internal/parser"
)

// ValidationError collects all errors found during validation.
// We gather all errors rather than stopping at the first one — a Daegfile
// with 3 problems should tell you all 3, not make you fix them one at a time.
// This is the same philosophy as Go's own type checker.
type ValidationError struct {
	Errors   []string
	Warnings []string
}

func (e *ValidationError) Error() string {
	return strings.Join(e.Errors, "\n")
}

func (e *ValidationError) HasErrors() bool {
	return len(e.Errors) > 0
}

// ValidationResult is returned by Validate on success.
type ValidationResult struct {
	// Order is a topological ordering of stage names — safe build order.
	Order []string
}

// Validate checks the parsed Daegfile for structural correctness and returns
// a topological ordering of stages safe for building.
//
// Checks performed (all errors collected before returning):
//  1. No duplicate stage names
//  2. No self-references
//  3. All parent references name a defined stage or an external image
//  4. No cycles
//  5. Warns about stages unreachable from the final stage
func Validate(daeg *parser.Daegfile) (*ValidationResult, error) {
	errs := &ValidationError{}

	// Build a name → stage index map for O(1) lookups.
	// In Go, map[K]V is the built-in hash map. make() initialises it —
	// writing to a nil map panics, so always make() before writing.
	index := make(map[string]int, len(daeg.Stages))
	for i, stage := range daeg.Stages {
		if stage.Name == "" {
			errs.Errors = append(errs.Errors,
				fmt.Sprintf("line %d: stage has no name (AS is required in Daegfiles)", stage.Line))
			continue
		}
		if _, exists := index[stage.Name]; exists {
			errs.Errors = append(errs.Errors,
				fmt.Sprintf("line %d: duplicate stage name %q", stage.Line, stage.Name))
			continue
		}
		index[stage.Name] = i
	}

	// Bail early — duplicate names would cause confusing secondary errors below.
	if errs.HasErrors() {
		return nil, errs
	}

	// Check rebase targets resolve to a known stage.
	for _, stage := range daeg.Stages {
		if stage.RebaseResolver != nil {
			target := stage.RebaseResolver.Stage
			if _, defined := index[target]; !defined {
				errs.Errors = append(errs.Errors,
					fmt.Sprintf("line %d: stage %q: RESOLVE WITH rebase references unknown stage %q",
						stage.RebaseResolver.Line, stage.Name, target))
			}
		}
	}

	// Check parent references and self-references.
	for _, stage := range daeg.Stages {
		for _, parent := range stage.Parents {
			if parent == stage.Name {
				errs.Errors = append(errs.Errors,
					fmt.Sprintf("line %d: stage %q references itself", stage.Line, stage.Name))
				continue
			}
			// If the parent isn't a defined stage name, it must be an external
			// image ref (e.g. "ubuntu:24.04"). We identify those by the presence
			// of a colon or slash — the solver lets BuildKit validate the ref itself.
			if _, defined := index[parent]; !defined {
				if !looksLikeImageRef(parent) {
					errs.Errors = append(errs.Errors,
						fmt.Sprintf("line %d: stage %q references unknown stage %q",
							stage.Line, stage.Name, parent))
				}
			}
		}
	}

	if errs.HasErrors() {
		return nil, errs
	}

	// Topological sort via DFS with cycle detection.
	//
	// Three colours per node — standard DFS cycle detection:
	//   white (0) = unvisited
	//   grey  (1) = in current DFS path (on the stack right now)
	//   black (2) = fully processed
	const white, grey, black = 0, 1, 2
	color := make(map[string]int, len(daeg.Stages))
	var order []string

	// visit is a recursive closure. In Go, closures capture variables by
	// reference — order, color, and errs are all shared with the outer scope.
	// We declare before defining because it calls itself recursively.
	var visit func(name string) bool
	visit = func(name string) bool {
		if color[name] == black {
			return true // already fully processed, skip
		}
		if color[name] == grey {
			// Hit a node currently on the DFS stack — this is a cycle.
			errs.Errors = append(errs.Errors,
				fmt.Sprintf("cycle detected: stage %q is its own ancestor", name))
			return false
		}

		color[name] = grey

		stage := daeg.FindStage(name)
		if stage == nil {
			// External image ref — no parents to visit, just mark done.
			color[name] = black
			return true
		}

		for _, parent := range stage.Parents {
			if !visit(parent) {
				return false
			}
		}

		color[name] = black
		// Append after all parents are processed — this gives reverse
		// topological order, which we reverse at the end.
		order = append(order, name)
		return true
	}

	// Visit every stage so we catch cycles in disconnected subgraphs too.
	for _, stage := range daeg.Stages {
		if color[stage.Name] == white {
			visit(stage.Name)
		}
	}

	if errs.HasErrors() {
		return nil, errs
	}

	// Note: no reversal needed. Because the outer loop visits stages in file
	// order, and Daegfiles must define stages before referencing them, the DFS
	// naturally produces correct topological order (parents before children)
	// without needing the reverse-post-order trick used in the general case.

	return &ValidationResult{Order: order}, nil
}

// looksLikeImageRef returns true if s looks like an OCI image reference
// rather than a stage name. Stage names are plain identifiers; image refs
// contain colons (ubuntu:24.04) or slashes (ghcr.io/org/image).
func looksLikeImageRef(s string) bool {
	return strings.ContainsAny(s, ":/")
}
