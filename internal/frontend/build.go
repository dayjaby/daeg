package frontend

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/moby/buildkit/client/llb"
	"github.com/moby/buildkit/frontend/dockerui"
	"github.com/moby/buildkit/frontend/gateway/client"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/dayjaby/daeg/internal/graph"
	"github.com/dayjaby/daeg/internal/parser"
)

const (
	localNameContext  = "context"
	localNameDaegfile = "dockerfile"
	defaultFilename   = "Daegfile"
)

// Build is the BuildKit frontend entrypoint.
func Build(ctx context.Context, c client.Client) (*client.Result, error) {
	// 1. Wrap the gateway client in dockerui.Client for Dockerfile2LLB delegation.
	uiClient, err := dockerui.NewClient(c)
	if err != nil {
		return nil, fmt.Errorf("failed to create dockerui client: %w", err)
	}

	// 2. Read the Daegfile.
	src, err := readDaegfile(ctx, c)
	if err != nil {
		return nil, err
	}

	// 3. ARG substitution — must happen before parsing so that
	//    MERGE(${STAGE_A}, ${STAGE_B}) resolves to real stage names.
	buildArgs := extractBuildArgs(c.BuildOpts().Opts)
	src = parser.SubstituteArgs(src, buildArgs)

	// 4. Parse into AST.
	daeg, err := parser.Parse(src)
	if err != nil {
		return nil, fmt.Errorf("parse error: %w", err)
	}

	// 5. Validate the DAG.
	if _, err := graph.Validate(daeg); err != nil {
		return nil, fmt.Errorf("validation error: %w", err)
	}

	// 6. Determine build target.
	targetName, err := resolveTarget(c, daeg)
	if err != nil {
		return nil, err
	}

	// 7. Build the local context state for COPY instructions.
	buildContext := llb.Local(localNameContext,
		llb.SessionID(c.BuildOpts().SessionID),
		llb.SharedKeyHint(localNameContext),
	)

	// 8. Compile AST → LLB, delegating instruction execution to Dockerfile2LLB.
	solver := NewSolver(daeg, uiClient, c)
	solver.SetBuildContext(buildContext)

	finalState, imgConfig, err := solver.Solve(ctx, targetName)
	if err != nil {
		return nil, fmt.Errorf("solve error: %w", err)
	}

	// 9. Marshal LLB and submit to BuildKit.
	def, err := finalState.Marshal(ctx, llb.LinuxAmd64)
	if err != nil {
		return nil, fmt.Errorf("marshal error: %w", err)
	}

	res, err := c.Solve(ctx, client.SolveRequest{
		Definition: def.ToPB(),
		Evaluate:   true,
	})
	if err != nil {
		return nil, fmt.Errorf("build error: %w", err)
	}

	// 10. Attach OCI image config — carries CMD, ENTRYPOINT, labels etc.
	//     Dockerfile2LLB populates this from the instructions it processed.
	if imgConfig != nil {
		ociConfig := ocispecs.Image{
			Config: ocispecs.ImageConfig{
				Cmd:          imgConfig.Config.Cmd,
				Entrypoint:   imgConfig.Config.Entrypoint,
				Env:          imgConfig.Config.Env,
				Labels:       imgConfig.Config.Labels,
				ExposedPorts: imgConfig.Config.ExposedPorts,
				WorkingDir:   imgConfig.Config.WorkingDir,
				User:         imgConfig.Config.User,
				StopSignal:   imgConfig.Config.StopSignal,
			},
		}
		configBytes, err := json.Marshal(ociConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal image config: %w", err)
		}
		res.AddMeta("containerimage.config", configBytes)
	}

	return res, nil
}

// readDaegfile fetches the raw Daegfile bytes from the build context.
func readDaegfile(ctx context.Context, c client.Client) (string, error) {
	opts := c.BuildOpts().Opts
	filename, ok := opts["filename"]
	if !ok {
		filename = defaultFilename
	}

	localSrc := llb.Local(localNameDaegfile,
		llb.IncludePatterns([]string{filename}),
		llb.SessionID(c.BuildOpts().SessionID),
		llb.SharedKeyHint(localNameDaegfile),
	)

	def, err := localSrc.Marshal(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to marshal local source: %w", err)
	}

	res, err := c.Solve(ctx, client.SolveRequest{Definition: def.ToPB()})
	if err != nil {
		return "", fmt.Errorf("failed to resolve build context: %w", err)
	}

	ref, err := res.SingleRef()
	if err != nil {
		return "", fmt.Errorf("failed to get context ref: %w", err)
	}

	data, err := ref.ReadFile(ctx, client.ReadRequest{Filename: filename})
	if err != nil {
		return "", fmt.Errorf("could not read %s: %w", filename, err)
	}

	return string(data), nil
}

// resolveTarget returns the stage name to build.
func resolveTarget(c client.Client, daeg *parser.Daegfile) (string, error) {
	if len(daeg.Stages) == 0 {
		return "", fmt.Errorf("Daegfile has no stages")
	}
	opts := c.BuildOpts().Opts
	if target, ok := opts["target"]; ok && target != "" {
		if daeg.FindStage(target) == nil {
			return "", fmt.Errorf("target stage %q not found", target)
		}
		return target, nil
	}
	return daeg.Stages[len(daeg.Stages)-1].Name, nil
}

// extractBuildArgs collects --build-arg values from BuildOpts.
// BuildKit passes them as "build-arg:<KEY>=<VALUE>" entries in Opts.
func extractBuildArgs(opts map[string]string) map[string]string {
	const prefix = "build-arg:"
	args := make(map[string]string)
	for k, v := range opts {
		if strings.HasPrefix(k, prefix) {
			key := strings.TrimPrefix(k, prefix)
			args[key] = v
		}
	}
	return args
}
