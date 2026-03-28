package parser

import (
	"strings"
	"testing"
)

const canonicalExample = `
FROM ubuntu:24.04 AS base
RUN apt-get update && apt-get install -y ca-certificates curl tzdata

FROM base AS cuda-tools
RUN apt-get install -y nvidia-cuda-toolkit

FROM base AS opencv
RUN apt-get install -y libopencv-dev

FROM base AS ffmpeg
RUN apt-get install -y ffmpeg libavcodec-dev

FROM MERGE(cuda-tools, opencv, ffmpeg)
RESOLVE WITH script ldconfig
RESOLVE WITH discard /var/lib/apt/lists /var/cache/apt /var/log /tmp
AS system-deps
RUN echo "system layer ready"

FROM system-deps AS python-app
RUN pip install torch numpy fastapi

FROM system-deps AS node-app
RUN npm install --prefix /app/ui

FROM MERGE(python-app, node-app)
AS final
COPY . .
CMD ["python", "server.py"]
`

func TestCanonicalExample(t *testing.T) {
	daeg, err := Parse(canonicalExample)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	wantStages := []string{
		"base", "cuda-tools", "opencv", "ffmpeg",
		"system-deps",
		"python-app", "node-app",
		"final",
	}
	if len(daeg.Stages) != len(wantStages) {
		t.Fatalf("expected %d stages, got %d", len(wantStages), len(daeg.Stages))
	}
	for i, want := range wantStages {
		if daeg.Stages[i].Name != want {
			t.Errorf("stage %d: expected %q, got %q", i, want, daeg.Stages[i].Name)
		}
	}

	// system-deps: one script resolver, one discard, no glob rules
	sd := daeg.FindStage("system-deps")
	if sd == nil {
		t.Fatal("stage system-deps not found")
	}
	if !sd.IsMerge() {
		t.Error("system-deps should be a merge stage")
	}
	if len(sd.ResolveRules) != 0 {
		t.Errorf("system-deps: expected 0 glob rules, got %d", len(sd.ResolveRules))
	}
	if len(sd.ScriptResolvers) != 1 || sd.ScriptResolvers[0].Cmd != "ldconfig" {
		t.Errorf("system-deps: expected script ldconfig, got %+v", sd.ScriptResolvers)
	}
	if len(sd.DiscardResolvers) != 1 {
		t.Fatalf("system-deps: expected 1 discard, got %d", len(sd.DiscardResolvers))
	}
	wantPaths := []string{"/var/lib/apt/lists", "/var/cache/apt", "/var/log", "/tmp"}
	for i, want := range wantPaths {
		if i >= len(sd.DiscardResolvers[0].Paths) || sd.DiscardResolvers[0].Paths[i] != want {
			t.Errorf("discard path %d: expected %q, got %q",
				i, want, sd.DiscardResolvers[0].Paths[i])
		}
	}

	// final: no resolve lines at all — branches asserted conflict-free
	final := daeg.FindStage("final")
	if final == nil {
		t.Fatal("stage final not found")
	}
	if !final.IsMerge() {
		t.Error("final should be a merge stage")
	}
	if len(final.ResolveRules) != 0 || len(final.ScriptResolvers) != 0 || len(final.DiscardResolvers) != 0 {
		t.Error("final should have no resolve directives")
	}
}

func TestDiscardMultiplePaths(t *testing.T) {
	src := `
FROM ubuntu:24.04 AS a
FROM ubuntu:24.04 AS b

FROM MERGE(a, b)
RESOLVE WITH discard /var/lib/apt/lists /var/cache/apt /var/log /tmp
AS combined
`
	daeg, err := Parse(src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	combined := daeg.FindStage("combined")
	if len(combined.DiscardResolvers) != 1 {
		t.Fatalf("expected 1 discard resolver, got %d", len(combined.DiscardResolvers))
	}
	if len(combined.DiscardResolvers[0].Paths) != 4 {
		t.Errorf("expected 4 paths, got %d", len(combined.DiscardResolvers[0].Paths))
	}
}

func TestDiscardRelativePathIsError(t *testing.T) {
	src := `
FROM ubuntu:24.04 AS a
FROM ubuntu:24.04 AS b

FROM MERGE(a, b)
RESOLVE WITH discard tmp
AS combined
`
	_, err := Parse(src)
	if err == nil {
		t.Error("expected error for relative discard path")
	}
}

func TestScriptNoQuotesNeeded(t *testing.T) {
	src := `
FROM ubuntu:24.04 AS a
FROM ubuntu:24.04 AS b

FROM MERGE(a, b)
RESOLVE WITH script update-ca-certificates --fresh
AS combined
`
	daeg, err := Parse(src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	combined := daeg.FindStage("combined")
	if combined.ScriptResolvers[0].Cmd != "update-ca-certificates --fresh" {
		t.Errorf("expected full command, got %q", combined.ScriptResolvers[0].Cmd)
	}
}

func TestAllThreeResolveVariantsTogether(t *testing.T) {
	src := `
FROM ubuntu:24.04 AS a
FROM ubuntu:24.04 AS b

FROM MERGE(a, b)
RESOLVE /etc/ld.so.conf.d/* STRATEGY union
RESOLVE /usr/bin/* STRATEGY priority
RESOLVE WITH script ldconfig
RESOLVE WITH script update-alternatives --auto ld
RESOLVE WITH discard /var/lib/apt/lists /tmp
RESOLVE WITH discard /var/log
AS combined
RUN echo "done"
`
	daeg, err := Parse(src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	combined := daeg.FindStage("combined")
	if len(combined.ResolveRules) != 2 {
		t.Errorf("expected 2 glob rules, got %d", len(combined.ResolveRules))
	}
	if len(combined.ScriptResolvers) != 2 {
		t.Errorf("expected 2 script resolvers, got %d", len(combined.ScriptResolvers))
	}
	if len(combined.DiscardResolvers) != 2 {
		t.Errorf("expected 2 discard resolvers, got %d", len(combined.DiscardResolvers))
	}
	// Declaration order preserved within each type
	if combined.ScriptResolvers[0].Cmd != "ldconfig" {
		t.Errorf("first script: expected ldconfig, got %q", combined.ScriptResolvers[0].Cmd)
	}
	if combined.DiscardResolvers[0].Paths[0] != "/var/lib/apt/lists" {
		t.Errorf("first discard path: expected /var/lib/apt/lists, got %q",
			combined.DiscardResolvers[0].Paths[0])
	}
}

func TestRebaseResolver(t *testing.T) {
	src := `
FROM ubuntu:24.04 AS base

FROM base AS base-apt
RUN apt-get update

FROM base-apt AS a
RUN apt-get install -y pkg-a

FROM base-apt AS b
RUN apt-get install -y pkg-b

FROM MERGE(a, b)
RESOLVE WITH rebase base
RESOLVE WITH script ldconfig
AS merged
`
	daeg, err := Parse(src)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	merged := daeg.FindStage("merged")
	if merged == nil {
		t.Fatal("stage merged not found")
	}
	if merged.RebaseResolver == nil {
		t.Fatal("expected a rebase resolver, got nil")
	}
	if merged.RebaseResolver.Stage != "base" {
		t.Errorf("expected rebase target %q, got %q", "base", merged.RebaseResolver.Stage)
	}
	if len(merged.ScriptResolvers) != 1 {
		t.Errorf("expected 1 script resolver, got %d", len(merged.ScriptResolvers))
	}
}

func TestRebaseDuplicateIsError(t *testing.T) {
	src := `
FROM ubuntu:24.04 AS base
FROM base AS a
RUN echo a
FROM base AS b
RUN echo b

FROM MERGE(a, b)
RESOLVE WITH rebase base
RESOLVE WITH rebase base
AS merged
`
	_, err := Parse(src)
	if err == nil {
		t.Error("expected error for duplicate RESOLVE WITH rebase")
	}
}

func TestResolveAfterAsIsError(t *testing.T) {
	src := `
FROM ubuntu:24.04 AS a
FROM ubuntu:24.04 AS b

FROM MERGE(a, b)
AS combined
RESOLVE WITH script ldconfig
`
	_, err := Parse(src)
	if err == nil {
		t.Error("expected error for RESOLVE after AS")
	}
}

func TestMergeRequiresTwoParents(t *testing.T) {
	src := `
FROM ubuntu:24.04 AS base

FROM MERGE(base)
AS combined
`
	_, err := Parse(src)
	if err == nil {
		t.Error("expected error for single-parent MERGE")
	}
}

func TestUnknownStrategyIsError(t *testing.T) {
	src := `
FROM ubuntu:24.04 AS a
FROM ubuntu:24.04 AS b

FROM MERGE(a, b)
RESOLVE /etc/* STRATEGY lasers
AS combined
`
	_, err := Parse(src)
	if err == nil {
		t.Error("expected error for unknown strategy")
	}
}

func TestCopyFromMergedStageRoundTrips(t *testing.T) {
	// COPY --from must work when the source is a merged stage.
	// From the parser's perspective it's just a raw instruction line —
	// the solver handles the lookup. We verify the line is preserved verbatim.
	src := `
FROM ubuntu:24.04 AS base

FROM base AS a
RUN echo a

FROM base AS b
RUN echo b

FROM MERGE(a, b)
AS merged

FROM ubuntu:24.04 AS consumer
COPY --from=merged /app /app
COPY --from=a /lib /lib
`
	daeg, err := Parse(src)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	consumer := daeg.FindStage("consumer")
	if consumer == nil {
		t.Fatal("stage consumer not found")
	}
	if len(consumer.RawLines) != 2 {
		t.Fatalf("expected 2 raw lines, got %d", len(consumer.RawLines))
	}

	// Raw lines must be preserved exactly — the solver and Dockerfile2LLB
	// need the original text, including --from= references.
	if !strings.Contains(consumer.RawLines[0], "--from=merged") {
		t.Errorf("expected --from=merged in first line, got: %q", consumer.RawLines[0])
	}
	if !strings.Contains(consumer.RawLines[1], "--from=a") {
		t.Errorf("expected --from=a in second line, got: %q", consumer.RawLines[1])
	}
}

func TestFromMergedStageThenBuildOn(t *testing.T) {
	// FROM <merged-stage> AS next must work — merged stages are first-class.
	src := `
FROM ubuntu:24.04 AS base

FROM base AS a
RUN echo a

FROM base AS b
RUN echo b

FROM MERGE(a, b)
AS merged

FROM merged AS final
RUN echo "building on merged"
`
	daeg, err := Parse(src)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	final := daeg.FindStage("final")
	if final == nil {
		t.Fatal("stage final not found")
	}
	if final.Parents[0] != "merged" {
		t.Errorf("expected parent merged, got %q", final.Parents[0])
	}
	if len(final.RawLines) != 1 {
		t.Fatalf("expected 1 instruction, got %d", len(final.RawLines))
	}
}
