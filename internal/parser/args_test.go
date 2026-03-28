package parser

import (
	"strings"
	"testing"
)

func TestSubstituteArgsBraceForm(t *testing.T) {
	src := `
ARG PYTHON_STAGE=python-app
ARG NODE_STAGE=node-app

FROM MERGE(${PYTHON_STAGE}, ${NODE_STAGE})
AS final
`
	result := SubstituteArgs(src, nil)
	if !strings.Contains(result, "FROM MERGE(python-app, node-app)") {
		t.Errorf("expected substituted MERGE line, got:\n%s", result)
	}
}

func TestSubstituteArgsDollarForm(t *testing.T) {
	src := `
ARG BASE=ubuntu:24.04
FROM $BASE AS root
`
	result := SubstituteArgs(src, nil)
	if !strings.Contains(result, "FROM ubuntu:24.04 AS root") {
		t.Errorf("expected substituted FROM line, got:\n%s", result)
	}
}

func TestBuildArgOverridesDefault(t *testing.T) {
	src := `
ARG BASE=ubuntu:24.04
FROM $BASE AS root
`
	result := SubstituteArgs(src, map[string]string{"BASE": "debian:12"})
	if !strings.Contains(result, "FROM debian:12 AS root") {
		t.Errorf("expected build-arg override, got:\n%s", result)
	}
}

func TestArgWithNoDefault(t *testing.T) {
	src := `
ARG STAGE
FROM MERGE(base, $STAGE)
AS final
`
	result := SubstituteArgs(src, nil)
	if !strings.Contains(result, "FROM MERGE(base, )") {
		t.Errorf("expected empty substitution, got:\n%s", result)
	}
}

func TestArgThenParseMerge(t *testing.T) {
	src := `
ARG A=stage-a
ARG B=stage-b

FROM ubuntu:24.04 AS stage-a
RUN echo a

FROM ubuntu:24.04 AS stage-b
RUN echo b

FROM MERGE(${A}, ${B})
AS final
`
	substituted := SubstituteArgs(src, nil)
	daeg, err := Parse(substituted)
	if err != nil {
		t.Fatalf("parse failed after substitution: %v", err)
	}
	final := daeg.FindStage("final")
	if final == nil {
		t.Fatal("stage final not found")
	}
	if final.Parents[0] != "stage-a" || final.Parents[1] != "stage-b" {
		t.Errorf("unexpected parents: %v", final.Parents)
	}
}
