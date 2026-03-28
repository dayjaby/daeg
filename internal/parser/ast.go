package parser

// MergeStrategy defines how file conflicts are resolved when merging parent layers.
type MergeStrategy int

const (
	StrategyUnknown  MergeStrategy = iota
	StrategyUnion                  // overlay both parents; last parent wins on conflict
	StrategyPriority               // left-most parent wins on any conflict
	StrategyError                  // explicit fail — same as default, but self-documenting
)

func (s MergeStrategy) String() string {
	switch s {
	case StrategyUnion:
		return "union"
	case StrategyPriority:
		return "priority"
	case StrategyError:
		return "error"
	default:
		return "unknown"
	}
}

// ResolveRule is a glob-scoped conflict resolution directive.
//
//	RESOLVE /etc/ld.so.conf.d/* STRATEGY union
//
// Any conflict not matched by any ResolveRule causes a build error (default=error).
type ResolveRule struct {
	Glob     string
	Strategy MergeStrategy
	Line     int
}

// ScriptResolver runs a command inside the merged container to repair
// derived files (caches, indexes) that each branch built independently.
// Runs in declaration order, after glob rules, before discards.
//
//	RESOLVE WITH script ldconfig
//	RESOLVE WITH script update-ca-certificates --fresh
type ScriptResolver struct {
	Cmd  string // full command, everything after "script "
	Line int
}

// DiscardResolver removes paths from the merged layer before sealing it.
// Replaces the per-branch "rm -rf" idiom — pay the cleanup cost once,
// at the merge point, rather than in every branch.
// Runs after all script resolvers, as the final step before sealing.
//
//	RESOLVE WITH discard /var/lib/apt/lists /var/cache/apt /var/log /tmp
type DiscardResolver struct {
	Paths []string // one or more absolute paths, recursive removal
	Line  int
}

// RebaseResolver changes the base of the merged snapshot.
// By default a MERGE uses the common base of its parents as the root layer.
// RESOLVE WITH rebase <stage> substitutes a different (typically earlier)
// stage as the root, while diffs are still computed against the original
// common base. This lets large transient layers (e.g. apt lists from an
// apt-updated stage) be excluded from the final image without whiteouts.
//
//	RESOLVE WITH rebase base
type RebaseResolver struct {
	Stage string // name of the stage to use as the new merge root
	Line  int
}

// Stage represents one named build stage.
//
// Simple stage:  FROM ubuntu:24.04 AS base
//
// Merge stage:
//
//	FROM MERGE(a, b, c)
//	RESOLVE /etc/* STRATEGY union
//	RESOLVE WITH script ldconfig
//	RESOLVE WITH discard /var/lib/apt/lists /tmp
//	AS combined
type Stage struct {
	Name    string
	Parents []string // len==1 for simple stages, len>=2 for merge stages

	ResolveRules     []ResolveRule
	ScriptResolvers  []ScriptResolver
	DiscardResolvers []DiscardResolver
	RebaseResolver   *RebaseResolver // nil if not specified; at most one allowed

	// RawLines holds instruction lines verbatim for BuildKit's own parser.
	RawLines []string

	Line int
}

// IsMerge reports whether this stage has multiple parents.
func (s *Stage) IsMerge() bool {
	return len(s.Parents) > 1
}

// Daegfile is the root result of a successful parse.
type Daegfile struct {
	Stages []*Stage
}

// FindStage returns the stage with the given name, or nil if not found.
func (d *Daegfile) FindStage(name string) *Stage {
	for _, s := range d.Stages {
		if s.Name == name {
			return s
		}
	}
	return nil
}
