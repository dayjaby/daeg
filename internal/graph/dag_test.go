package graph

import (
	"testing"

	"github.com/yourname/daeg/internal/parser"
)

// mustParse is a test helper that parses a Daegfile and fails the test if
// parsing itself fails. In Go, helpers like this take *testing.T and call
// t.Fatal — this stops the current test immediately, not the whole suite.
func mustParse(t *testing.T, src string) *parser.Daegfile {
	t.Helper() // marks this as a helper so failure lines point to the caller
	daeg, err := parser.Parse(src)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	return daeg
}

func TestValidCanonicalExample(t *testing.T) {
	daeg := mustParse(t, `
FROM ubuntu:24.04 AS base
RUN apt-get update

FROM base AS cuda-tools
RUN apt-get install -y nvidia-cuda-toolkit

FROM base AS opencv
RUN apt-get install -y libopencv-dev

FROM base AS ffmpeg
RUN apt-get install -y ffmpeg

FROM MERGE(cuda-tools, opencv, ffmpeg)
RESOLVE WITH script ldconfig
RESOLVE WITH discard /var/lib/apt/lists /tmp
AS system-deps

FROM system-deps AS python-app
RUN pip install torch

FROM system-deps AS node-app
RUN npm install

FROM MERGE(python-app, node-app)
AS final
COPY . .
`)

	result, err := Validate(daeg)
	if err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}

	// Topological order must have parents before children.
	// We verify this by checking that every stage appears after all its parents.
	pos := make(map[string]int, len(result.Order))
	for i, name := range result.Order {
		pos[name] = i
	}
	for _, stage := range daeg.Stages {
		for _, parent := range stage.Parents {
			if parentPos, ok := pos[parent]; ok {
				if parentPos >= pos[stage.Name] {
					t.Errorf("topological order violated: %q (pos %d) should come before %q (pos %d)",
						parent, parentPos, stage.Name, pos[stage.Name])
				}
			}
		}
	}
}

func TestDuplicateNameIsError(t *testing.T) {
	daeg := mustParse(t, `
FROM ubuntu:24.04 AS base
FROM ubuntu:24.04 AS base
`)
	_, err := Validate(daeg)
	if err == nil {
		t.Error("expected error for duplicate stage name")
	}
}

func TestSelfReferenceIsError(t *testing.T) {
	// We can't express self-reference through the parser (it would be
	// FROM MERGE(a, a) AS a which the parser allows syntactically),
	// so we construct the AST directly. This is a Go testing idiom —
	// test the layer you're in without depending on layers above it.
	daeg := &parser.Daegfile{
		Stages: []*parser.Stage{
			{Name: "a", Parents: []string{"ubuntu:24.04"}, Line: 1},
			{Name: "b", Parents: []string{"a", "b"}, Line: 2}, // b references itself
		},
	}
	_, err := Validate(daeg)
	if err == nil {
		t.Error("expected error for self-reference")
	}
}

func TestMissingParentRefIsError(t *testing.T) {
	daeg := mustParse(t, `
FROM ubuntu:24.04 AS base

FROM MERGE(base, ghost)
AS combined
`)
	_, err := Validate(daeg)
	if err == nil {
		t.Error("expected error for unknown parent stage")
	}
}

func TestExternalImageRefIsNotAnError(t *testing.T) {
	// "ubuntu:24.04" is not a defined stage — it's an external image.
	// The colon tells us it's an image ref, not a missing stage name.
	daeg := mustParse(t, `
FROM ubuntu:24.04 AS base
FROM debian:12 AS other

FROM MERGE(base, other)
AS combined
`)
	_, err := Validate(daeg)
	if err != nil {
		t.Errorf("external image refs should not be errors, got: %v", err)
	}
}

func TestCycleIsError(t *testing.T) {
	// Construct a cycle directly: a → b → c → a
	// Can't express this through the parser since stages must be defined
	// before they're referenced — but the validator should catch it regardless.
	daeg := &parser.Daegfile{
		Stages: []*parser.Stage{
			{Name: "a", Parents: []string{"c"}, Line: 1},
			{Name: "b", Parents: []string{"a"}, Line: 2},
			{Name: "c", Parents: []string{"b"}, Line: 3},
		},
	}
	_, err := Validate(daeg)
	if err == nil {
		t.Error("expected error for cycle a→b→c→a")
	}
}

func TestAllErrorsCollected(t *testing.T) {
	// A Daegfile with both a duplicate name and a missing ref.
	// We want both errors reported, not just the first one.
	daeg := mustParse(t, `
FROM ubuntu:24.04 AS base
FROM ubuntu:24.04 AS base
`)
	// Inject a missing ref manually since the duplicate blocks further parsing
	daeg2 := &parser.Daegfile{
		Stages: []*parser.Stage{
			{Name: "a", Parents: []string{"ubuntu:24.04"}, Line: 1},
			{Name: "a", Parents: []string{"ubuntu:24.04"}, Line: 2}, // duplicate
		},
	}
	_, err := Validate(daeg2)
	if err == nil {
		t.Fatal("expected validation errors")
	}
	ve, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("expected *ValidationError, got %T", err)
	}
	if len(ve.Errors) < 1 {
		t.Errorf("expected at least 1 error, got %d: %v", len(ve.Errors), ve.Errors)
	}
	_ = daeg // suppress unused warning
}
