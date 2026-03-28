package frontend

import (
	"context"
	"fmt"
	"strings"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/dockerfile/dockerfile2llb"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/frontend/gateway/client"
	dockerspec "github.com/moby/docker-image-spec/specs-go/v1"

	"github.com/dayjaby/daeg/internal/parser"
)

// Solver translates a validated Daegfile AST into a tree of llb.State values.
//
// Dockerfile2LLB cannot accept a pre-built llb.State as a FROM base —
// MainContext is the COPY build context, not the FROM base. So we use two
// strategies depending on whether a stage's ancestry reaches an external image:
//
//   - External-ancestry stages: reconstruct the full ancestor chain as a
//     multi-stage Dockerfile and pass it to Dockerfile2LLB once. It handles
//     all internal FROM references (and all Dockerfile syntax including
//     heredocs) natively.
//
//   - Post-merge stages (base is a pre-built merged state): applyRawLines
//     applies instructions directly on the resolved llb.State.
type Solver struct {
	daeg         *parser.Daegfile
	resolved     map[string]llb.State                  // stage name → fully built llb.State
	imageConfigs map[string]*dockerspec.DockerOCIImage // stage name → image metadata
	buildContext llb.State                             // local build context for COPY
	uiClient     *dockerui.Client                      // provides build context + named contexts
	gwClient     client.Client                         // gateway client used as MetaResolver
}

func NewSolver(daeg *parser.Daegfile, uiClient *dockerui.Client, gwClient client.Client) *Solver {
	return &Solver{
		daeg:         daeg,
		resolved:     make(map[string]llb.State),
		imageConfigs: make(map[string]*dockerspec.DockerOCIImage),
		uiClient:     uiClient,
		gwClient:     gwClient,
	}
}

// SetBuildContext injects the local build context state.
func (s *Solver) SetBuildContext(ctx llb.State) {
	s.buildContext = ctx
}

// Solve resolves the target stage and returns its llb.State and image config.
func (s *Solver) Solve(ctx context.Context, targetName string) (llb.State, *dockerspec.DockerOCIImage, error) {
	stage := s.daeg.FindStage(targetName)
	if stage == nil {
		return llb.State{}, nil, fmt.Errorf("stage %q not found", targetName)
	}
	if err := s.resolveStage(ctx, stage); err != nil {
		return llb.State{}, nil, err
	}
	return s.resolved[targetName], s.imageConfigs[targetName], nil
}

// resolveStage recursively resolves a stage, memoising the result.
func (s *Solver) resolveStage(ctx context.Context, stage *parser.Stage) error {
	if _, ok := s.resolved[stage.Name]; ok {
		return nil // already resolved
	}

	// Resolve all parents first — DFS ensures parents are ready before children.
	for _, parentName := range stage.Parents {
		parent := s.daeg.FindStage(parentName)
		if parent == nil {
			continue // external image ref — llb.Image handles it
		}
		if err := s.resolveStage(ctx, parent); err != nil {
			return err
		}
	}

	if stage.IsMerge() {
		return s.resolveMergeStage(ctx, stage)
	}
	return s.resolveSimpleStage(ctx, stage)
}

// resolveSimpleStage handles a single-parent FROM stage.
//
// If the stage's ancestry traces back to an external image without crossing a
// merge boundary, we reconstruct the full ancestor chain as a multi-stage
// Dockerfile and delegate to Dockerfile2LLB once. It resolves all internal
// FROM references natively and supports all Dockerfile syntax including
// heredocs, multi-line RUN, SHELL, etc.
//
// If the ancestry crosses a merge boundary (the base is a pre-built merged
// state), we fall back to applyRawLines — Dockerfile2LLB has no way to accept
// a pre-built llb.State as a FROM base.
func (s *Solver) resolveSimpleStage(ctx context.Context, stage *parser.Stage) error {
	if s.hasMergeAncestor(stage.Parents[0]) {
		// Base is a merge output — apply instructions directly on it.
		base := s.resolved[stage.Parents[0]]
		st, err := s.applyRawLines(base, stage)
		if err != nil {
			return fmt.Errorf("stage %q: %w", stage.Name, err)
		}
		s.resolved[stage.Name] = st
		return nil
	}

	// Build the complete ancestor chain back to the external base image.
	// Passing the whole chain to Dockerfile2LLB means it handles FROM
	// references between stages internally — no registry lookup for stage names.
	chain := s.buildExternalChain(stage)
	dockerfile := s.reconstructChainDockerfile(chain)

	opt := dockerfile2llb.ConvertOpt{
		Client:       s.uiClient,
		MetaResolver: s.gwClient,
	}
	st, img, _, _, err := dockerfile2llb.Dockerfile2LLB(ctx, []byte(dockerfile), opt)
	if err != nil {
		return fmt.Errorf("stage %q: Dockerfile2LLB: %w", stage.Name, err)
	}
	if st == nil {
		return fmt.Errorf("stage %q: Dockerfile2LLB returned nil state", stage.Name)
	}
	s.resolved[stage.Name] = *st
	s.imageConfigs[stage.Name] = img
	return nil
}

// hasMergeAncestor reports whether the named stage is or transitively descends
// from a merge stage (as opposed to tracing back cleanly to an external image).
func (s *Solver) hasMergeAncestor(name string) bool {
	stage := s.daeg.FindStage(name)
	if stage == nil {
		return false // external image ref
	}
	if stage.IsMerge() {
		return true
	}
	return s.hasMergeAncestor(stage.Parents[0])
}

// buildExternalChain walks the ancestry of target upward through simple stages
// until it reaches an external image ref, returning the chain in top-down order.
func (s *Solver) buildExternalChain(target *parser.Stage) []*parser.Stage {
	var chain []*parser.Stage
	var walk func(name string)
	walk = func(name string) {
		stage := s.daeg.FindStage(name)
		if stage == nil || stage.IsMerge() {
			return
		}
		walk(stage.Parents[0])
		chain = append(chain, stage)
	}
	walk(target.Name)
	return chain
}

// reconstructChainDockerfile builds a multi-stage Dockerfile from the chain.
// Each stage's RawLines are emitted verbatim so heredocs and all syntax
// features pass through to Dockerfile2LLB unchanged.
func (s *Solver) reconstructChainDockerfile(chain []*parser.Stage) string {
	var sb strings.Builder
	for _, stage := range chain {
		sb.WriteString("FROM ")
		sb.WriteString(stage.Parents[0])
		sb.WriteString(" AS ")
		sb.WriteString(stage.Name)
		sb.WriteString("\n")
		for _, line := range stage.RawLines {
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// resolveMergeStage handles FROM MERGE(...) stages.
func (s *Solver) resolveMergeStage(ctx context.Context, stage *parser.Stage) error {
	// Resolve parent states.
	parentStates := make([]llb.State, len(stage.Parents))
	for i, parentName := range stage.Parents {
		st, err := s.resolveRef(parentName)
		if err != nil {
			return fmt.Errorf("stage %q: resolving parent %q: %w",
				stage.Name, parentName, err)
		}
		parentStates[i] = st
	}

	// RESOLVE WITH rebase: use a different stage as the merge root while still
	// computing diffs against the common base. This excludes transient layers
	// (e.g. apt lists from an apt-updated stage) from the final image entirely
	// — no whiteouts, no hidden data — because those files only exist in the
	// common base layer which is replaced by the rebase target.
	var rebaseTarget *llb.State
	if stage.RebaseResolver != nil {
		st, err := s.resolveRef(stage.RebaseResolver.Stage)
		if err != nil {
			return fmt.Errorf("stage %q: rebase target %q: %w",
				stage.Name, stage.RebaseResolver.Stage, err)
		}
		rebaseTarget = &st
	}

	// Compute the merge using Diff + Merge when a common base exists,
	// or a direct Merge otherwise.
	merged, err := s.computeMerge(stage.Parents, parentStates, rebaseTarget)
	if err != nil {
		return err
	}

	// Apply RESOLVE WITH script — run repair commands inside the merged container.
	// These run before discards so scripts can read paths that will be removed.
	for _, script := range stage.ScriptResolvers {
		merged = merged.Run(llb.Shlex(script.Cmd)).Root()
	}

	// Apply RESOLVE WITH discard.
	merged, err = s.applyDiscards(merged, stage.DiscardResolvers)
	if err != nil {
		return err
	}

	// Apply any instructions on top of the merged+cleaned state.
	if len(stage.RawLines) > 0 {
		st, err := s.applyRawLines(merged, stage)
		if err != nil {
			return fmt.Errorf("stage %q: %w", stage.Name, err)
		}
		s.resolved[stage.Name] = st
	} else {
		s.resolved[stage.Name] = merged
	}
	return nil
}

// computeMerge computes the merged llb.State from multiple parent states.
//
// When rebaseTarget is set, each branch is diffed against its own direct
// parent rather than the LCA. This means only what that stage itself added
// (e.g. "make install", "dpkg -i") enters the merge — compilers, source trees,
// and apt lists that live in parent stages are excluded without any COPY --from.
// The rebase target is used as the merge root.
//
// Without a rebase target the classic behaviour applies: all branches must
// share the same direct parent, which is used as both root and diff base.
func (s *Solver) computeMerge(parentNames []string, parentStates []llb.State, rebaseTarget *llb.State) (llb.State, error) {
	if rebaseTarget != nil {
		inputs := []llb.State{*rebaseTarget}
		for i, name := range parentNames {
			branch := s.daeg.FindStage(name)
			if branch == nil || len(branch.Parents) == 0 {
				// External image ref — diff against scratch to get all its files.
				inputs = append(inputs, llb.Diff(llb.Scratch(), parentStates[i]))
				continue
			}
			if branch.IsMerge() {
				// Merge stages have no single direct parent. Diff against the
				// rebase target instead — correct as long as rebaseTarget is an
				// ancestor, which the rebase contract requires.
				inputs = append(inputs, llb.Diff(*rebaseTarget, parentStates[i]))
				continue
			}
			directParent, err := s.resolveRef(branch.Parents[0])
			if err != nil {
				return llb.State{}, fmt.Errorf("stage %q: resolving parent %q: %w",
					name, branch.Parents[0], err)
			}
			inputs = append(inputs, llb.Diff(directParent, parentStates[i]))
		}
		return llb.Merge(inputs), nil
	}

	commonBase, hasCommon := s.findCommonBase(parentNames)
	if hasCommon {
		origBase := s.resolved[commonBase]
		inputs := []llb.State{origBase}
		for _, ps := range parentStates {
			inputs = append(inputs, llb.Diff(origBase, ps))
		}
		return llb.Merge(inputs), nil
	}
	return llb.Merge(parentStates), nil
}

// applyDiscards removes paths from a state by chaining Rm FileOps.
func (s *Solver) applyDiscards(st llb.State, discards []parser.DiscardResolver) (llb.State, error) {
	if len(discards) == 0 {
		return st, nil
	}

	var allPaths []string
	for _, d := range discards {
		allPaths = append(allPaths, d.Paths...)
	}
	if len(allPaths) == 0 {
		return st, nil
	}

	// WithAllowWildcard is required for glob patterns like /var/lib/apt/lists/*.
	action := llb.Rm(allPaths[0], llb.WithAllowNotFound(true), llb.WithAllowWildcard(true))
	for _, path := range allPaths[1:] {
		action = action.Rm(path, llb.WithAllowNotFound(true), llb.WithAllowWildcard(true))
	}
	return st.File(action), nil
}

// resolveRef returns the llb.State for a name that is either a resolved
// stage or an external image reference.
func (s *Solver) resolveRef(name string) (llb.State, error) {
	if st, ok := s.resolved[name]; ok {
		return st, nil
	}
	return llb.Image(name), nil
}

// applyRawLines applies a stage's instructions directly on a base llb.State.
// Used for post-merge stages where Dockerfile2LLB cannot be used (no way to
// inject a pre-built llb.State as a FROM base).
// Supports RUN, ENV, WORKDIR, USER, COPY (including --from). Image-config-only
// instructions (CMD, ENTRYPOINT, EXPOSE, LABEL, ARG, STOPSIGNAL) are silently
// skipped — they have no effect on the filesystem layer.
func (s *Solver) applyRawLines(base llb.State, stage *parser.Stage) (llb.State, error) {
	st := base
	for _, raw := range joinContinuations(stage.RawLines) {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		upper := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(upper, "RUN "):
			cmd := strings.TrimSpace(line[4:])
			st = st.Run(llb.Args([]string{"/bin/sh", "-c", cmd})).Root()
		case strings.HasPrefix(upper, "ENV "):
			rest := strings.TrimSpace(line[4:])
			k, v, _ := strings.Cut(rest, "=")
			if !strings.Contains(rest, "=") {
				parts := strings.SplitN(rest, " ", 2)
				if len(parts) == 2 {
					k, v = parts[0], strings.TrimSpace(parts[1])
				}
			}
			st = st.AddEnv(strings.TrimSpace(k), strings.TrimSpace(v))
		case strings.HasPrefix(upper, "WORKDIR "):
			st = st.Dir(strings.TrimSpace(line[8:]))
		case strings.HasPrefix(upper, "USER "):
			st = st.User(strings.TrimSpace(line[5:]))
		case strings.HasPrefix(upper, "COPY "):
			rest := strings.TrimSpace(line[5:])
			if strings.HasPrefix(strings.ToUpper(rest), "--FROM=") {
				eq := strings.Index(rest, "=")
				fromName := strings.Fields(rest[eq+1:])[0]
				remainder := strings.TrimSpace(rest[eq+1+len(fromName):])
				parts := strings.Fields(remainder)
				if len(parts) < 2 {
					return st, fmt.Errorf("COPY --from requires src and dest")
				}
				src, dest := parts[0], parts[len(parts)-1]
				fromState, err := s.resolveRef(fromName)
				if err != nil {
					return st, err
				}
				st = st.File(llb.Copy(fromState, src, dest,
					&llb.CopyInfo{AllowWildcard: true, CreateDestPath: true}))
			} else {
				parts := strings.Fields(rest)
				if len(parts) < 2 {
					return st, fmt.Errorf("COPY requires src and dest")
				}
				src, dest := parts[0], parts[len(parts)-1]
				st = st.File(llb.Copy(s.buildContext, src, dest,
					&llb.CopyInfo{AllowWildcard: true, CreateDestPath: true}))
			}
		case strings.HasPrefix(upper, "CMD "),
			strings.HasPrefix(upper, "ENTRYPOINT "),
			strings.HasPrefix(upper, "LABEL "),
			strings.HasPrefix(upper, "EXPOSE "),
			strings.HasPrefix(upper, "ARG "),
			strings.HasPrefix(upper, "STOPSIGNAL "),
			strings.HasPrefix(upper, "SHELL "):
			// image-config-only — no filesystem effect, skip in LLB path
		default:
			return st, fmt.Errorf("unsupported instruction in post-merge stage %q: %q", stage.Name, line)
		}
	}
	return st, nil
}

// joinContinuations merges backslash-continued lines into single logical lines.
func joinContinuations(rawLines []string) []string {
	var result []string
	var current strings.Builder
	for _, raw := range rawLines {
		stripped := strings.TrimRight(raw, " \t")
		if strings.HasSuffix(stripped, "\\") {
			current.WriteString(stripped[:len(stripped)-1])
			current.WriteString(" ")
		} else {
			current.WriteString(raw)
			result = append(result, current.String())
			current.Reset()
		}
	}
	if current.Len() > 0 {
		result = append(result, current.String())
	}
	return result
}

// findCommonBase returns the name of a stage that is a direct single parent
// of all given stage names, enabling the Diff pattern for cache independence.
func (s *Solver) findCommonBase(parentNames []string) (string, bool) {
	if len(parentNames) < 2 {
		return "", false
	}
	first := s.daeg.FindStage(parentNames[0])
	if first == nil || len(first.Parents) != 1 {
		return "", false
	}
	candidate := first.Parents[0]
	for _, name := range parentNames[1:] {
		stage := s.daeg.FindStage(name)
		if stage == nil || len(stage.Parents) != 1 || stage.Parents[0] != candidate {
			return "", false
		}
	}
	return candidate, true
}
