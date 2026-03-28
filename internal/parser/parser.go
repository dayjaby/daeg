package parser

import (
	"fmt"
	"strings"
)

// parseState is the parser's current mode.
type parseState int

const (
	stateNormal       parseState = iota
	stateMergeHeader
	stateInstructions
)

// ParseError carries the source line number for user-facing error messages.
type ParseError struct {
	Line    int
	Message string
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("line %d: %s", e.Line, e.Message)
}

// Parse takes the raw text of a Daegfile and returns the AST or an error.
func Parse(src string) (*Daegfile, error) {
	lines := strings.Split(src, "\n")
	daeg := &Daegfile{}
	state := stateNormal
	var current *Stage

	for lineNum, raw := range lines {
		n := lineNum + 1
		line := strings.TrimSpace(raw)

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		upper := strings.ToUpper(line)

		switch state {

		case stateNormal:
			// ARG lines at the top level are consumed by SubstituteArgs before
			// Parse is called. We skip them here rather than erroring — this
			// allows ARG declarations to appear freely between stages too,
			// matching Dockerfile semantics.
			if strings.HasPrefix(upper, "ARG ") || upper == "ARG" {
				continue
			}

			if !strings.HasPrefix(upper, "FROM ") && upper != "FROM" {
				return nil, &ParseError{n, fmt.Sprintf("expected FROM, got: %q", line)}
			}
			if current != nil {
				daeg.Stages = append(daeg.Stages, current)
			}

			rest := strings.TrimSpace(line[4:])
			if strings.HasPrefix(strings.ToUpper(rest), "MERGE(") {
				parents, err := parseMergeList(rest, n)
				if err != nil {
					return nil, err
				}
				current = &Stage{Parents: parents, Line: n}
				state = stateMergeHeader
			} else {
				image, name, err := parseSimpleFrom(rest, n)
				if err != nil {
					return nil, err
				}
				current = &Stage{Parents: []string{image}, Name: name, Line: n}
				state = stateInstructions
			}

		case stateMergeHeader:
			switch {

			case strings.HasPrefix(upper, "RESOLVE WITH REBASE "):
				stageName := strings.TrimSpace(line[len("RESOLVE WITH rebase "):])
				if stageName == "" {
					return nil, &ParseError{n, "RESOLVE WITH rebase requires a stage name"}
				}
				if current.RebaseResolver != nil {
					return nil, &ParseError{n, "only one RESOLVE WITH rebase is allowed per merge stage"}
				}
				current.RebaseResolver = &RebaseResolver{Stage: stageName, Line: n}

			case strings.HasPrefix(upper, "RESOLVE WITH SCRIPT "):
				// Everything after "RESOLVE WITH script " is the command — no quotes needed
				// because there is nothing after the command on this line.
				cmd := strings.TrimSpace(line[len("RESOLVE WITH script "):])
				if cmd == "" {
					return nil, &ParseError{n, "RESOLVE WITH script requires a command"}
				}
				current.ScriptResolvers = append(current.ScriptResolvers,
					ScriptResolver{Cmd: cmd, Line: n})

			case strings.HasPrefix(upper, "RESOLVE WITH DISCARD "):
				// Everything after "RESOLVE WITH discard " is a space-separated path list.
				rest := strings.TrimSpace(line[len("RESOLVE WITH discard "):])
				paths := strings.Fields(rest)
				if len(paths) == 0 {
					return nil, &ParseError{n, "RESOLVE WITH discard requires at least one path"}
				}
				for _, p := range paths {
					if !strings.HasPrefix(p, "/") {
						return nil, &ParseError{n, fmt.Sprintf(
							"discard path %q must be absolute (start with /)", p)}
					}
				}
				current.DiscardResolvers = append(current.DiscardResolvers,
					DiscardResolver{Paths: paths, Line: n})

			case strings.HasPrefix(upper, "RESOLVE "):
				rule, err := parseResolveRule(line, n)
				if err != nil {
					return nil, err
				}
				current.ResolveRules = append(current.ResolveRules, rule)

			case strings.HasPrefix(upper, "AS "):
				name := strings.TrimSpace(line[3:])
				if name == "" {
					return nil, &ParseError{n, "AS requires a stage name"}
				}
				current.Name = name
				state = stateInstructions

			default:
				return nil, &ParseError{n, fmt.Sprintf(
					"unexpected %q in merge header: only RESOLVE and AS are allowed here",
					line,
				)}
			}

		case stateInstructions:
			if strings.HasPrefix(upper, "FROM ") || upper == "FROM" {
				daeg.Stages = append(daeg.Stages, current)
				current = nil
				state = stateNormal

				tail, err := Parse(strings.Join(lines[lineNum:], "\n"))
				if err != nil {
					return nil, err
				}
				daeg.Stages = append(daeg.Stages, tail.Stages...)
				return daeg, nil

			} else if strings.HasPrefix(upper, "RESOLVE ") {
				return nil, &ParseError{n, "RESOLVE must appear before AS in a merge stage"}
			} else {
				current.RawLines = append(current.RawLines, raw)
			}
		}
	}

	if current != nil {
		daeg.Stages = append(daeg.Stages, current)
	}

	return daeg, nil
}

// parseResolveRule parses: RESOLVE /etc/* STRATEGY union
func parseResolveRule(line string, n int) (ResolveRule, error) {
	fields := strings.Fields(line)
	if len(fields) != 4 {
		return ResolveRule{}, &ParseError{n,
			"RESOLVE syntax: RESOLVE <glob> STRATEGY <union|priority|error>"}
	}
	if strings.ToUpper(fields[2]) != "STRATEGY" {
		return ResolveRule{}, &ParseError{n,
			fmt.Sprintf("expected STRATEGY, got %q", fields[2])}
	}
	strategy, err := parseStrategy(fields[3], n)
	if err != nil {
		return ResolveRule{}, err
	}
	return ResolveRule{Glob: fields[1], Strategy: strategy, Line: n}, nil
}

// parseStrategy converts a strategy string to its typed constant.
func parseStrategy(s string, n int) (MergeStrategy, error) {
	switch strings.ToLower(s) {
	case "union":
		return StrategyUnion, nil
	case "priority":
		return StrategyPriority, nil
	case "error":
		return StrategyError, nil
	default:
		return StrategyUnknown, &ParseError{n,
			fmt.Sprintf("unknown strategy %q: must be union, priority, or error", s)}
	}
}

// parseMergeList parses "MERGE(a, b, c)" → ["a", "b", "c"]
func parseMergeList(s string, n int) ([]string, error) {
	if !strings.HasSuffix(s, ")") {
		return nil, &ParseError{n, "MERGE(...) is missing closing ')'"}
	}
	inner := s[len("MERGE(") : len(s)-1]
	parts := strings.Split(inner, ",")

	var parents []string
	for _, p := range parts {
		name := strings.TrimSpace(p)
		if name == "" {
			return nil, &ParseError{n, "empty stage name inside MERGE(...)"}
		}
		parents = append(parents, name)
	}
	if len(parents) < 1 {
		return nil, &ParseError{n, "MERGE requires at least 1 parent stage"}
	}
	return parents, nil
}

// parseSimpleFrom parses "ubuntu:24.04 AS base" → ("ubuntu:24.04", "base", nil)
func parseSimpleFrom(s string, n int) (image, name string, err error) {
	parts := strings.Fields(s)
	switch len(parts) {
	case 1:
		return parts[0], "", nil
	case 3:
		if strings.ToUpper(parts[1]) != "AS" {
			return "", "", &ParseError{n, fmt.Sprintf("expected AS, got %q", parts[1])}
		}
		return parts[0], parts[2], nil
	default:
		return "", "", &ParseError{n, fmt.Sprintf("invalid FROM syntax: %q", s)}
	}
}
