# Daeg ᛞ

A BuildKit frontend that extends Dockerfile syntax with explicit DAG-based layer merging.

Daeg lets you declare multiple parent stages and merge them into a single layer — with control over conflict resolution, repair scripts, and build-time cleanup. It is a strict superset of Dockerfile syntax: every valid Dockerfile is a valid Daegfile.

---

## The problem

Docker builds are a linked list:

```
ubuntu:24.04 → system-deps → python-deps → app
```

If you need both Python and Node in one image, you install them sequentially. A change to the Node installation invalidates the Python cache even though the two are independent. And every branch that runs `apt-get` must clean up after itself.

## The solution

Daeg makes the build graph explicit:

```
ubuntu:24.04
├── cuda-tools
├── opencv
└── ffmpeg
      ↓
  MERGE → system-deps → python-app ─┐
                      └── node-app  ├→ MERGE → final
                                    ┘
```

Independent branches build in parallel. The merge is content-addressed — identical files across branches are deduplicated automatically. Cleanup happens once at the merge point.

---

## Quick start

```bash
# Build and push the frontend image
docker buildx build -t ghcr.io/yourname/daeg:latest --push .

# Use it — add the syntax line to your Daegfile
echo '# syntax=ghcr.io/yourname/daeg:latest' | cat - Daegfile | \
  docker buildx build -f - .

# Target a specific stage
docker buildx build --target system-deps -f Daegfile .

# Pass build arguments
docker buildx build --build-arg BASE=debian:12 -f Daegfile .
```

---

## Syntax

Daeg adds three new constructs to Dockerfile syntax. Everything else (`RUN`, `COPY`, `ENV`, `ENTRYPOINT`, etc.) is unchanged and delegated to BuildKit's own parser.

### Merge stage

```dockerfile
FROM MERGE(stage-a, stage-b, stage-c)
RESOLVE WITH script ldconfig
RESOLVE WITH discard /var/lib/apt/lists /var/cache/apt /tmp
AS combined
RUN echo "all dependencies available"
```

The merge header opens after `FROM MERGE(...)` and closes with `AS <name>`. Only `RESOLVE` directives are allowed in the header.

### RESOLVE directives

Three forms, all optional:

```dockerfile
# Glob-scoped conflict strategy
RESOLVE /etc/ld.so.conf.d/* STRATEGY union
RESOLVE /usr/bin/*           STRATEGY priority

# Global repair script (runs after glob rules, before discards)
RESOLVE WITH script ldconfig
RESOLVE WITH script update-ca-certificates --fresh

# Remove build-time artifact paths (runs last, before sealing)
RESOLVE WITH discard /var/lib/apt/lists /var/cache/apt /var/log /tmp
```

Conflict strategies:

| Strategy | Behaviour |
|---|---|
| `union` | Overlay both parents — last parent wins on conflict |
| `priority` | Left-most parent wins on any conflict |
| `error` | Fail the build on conflict (same as default, but explicit) |

**Default behaviour:** any conflict not matched by a glob rule causes a build error. Identical bytes on both sides are never a conflict. Different filenames in the same directory are not a conflict — both are kept.

### ARG in MERGE

```dockerfile
ARG PYTHON_STAGE=python-app
ARG NODE_STAGE=node-app

FROM MERGE(${PYTHON_STAGE}, ${NODE_STAGE})
AS final
```

`--build-arg` values override defaults:

```bash
docker buildx build --build-arg PYTHON_STAGE=python-gpu -f Daegfile .
```

### Building on a merged stage

Merged stages are first-class. You can use them as a base for subsequent stages:

```dockerfile
FROM merged-stage AS next
RUN echo "building on top of the merge"
```

And copy from them:

```dockerfile
FROM ubuntu:24.04 AS consumer
COPY --from=merged-stage /app /app
```

---

## Execution order inside a merge stage

1. Apply filesystem merge across all parents
2. Check every conflict against glob RESOLVE rules (error if unmatched)
3. Run `RESOLVE WITH script` commands in declaration order
4. Apply `RESOLVE WITH discard` paths
5. Seal the layer
6. Execute instructions (`RUN`, `COPY`, `ENV`, ...)

---

## Why the Diff + Merge pattern

When all parents share a common base, Daeg uses `llb.Diff` before merging:

```
base ──→ branch-a  →  Diff(base, branch-a)  ──┐
                                                ├─→ Merge(base, diffA, diffB)
base ──→ branch-b  →  Diff(base, branch-b)  ──┘
```

This isolates what each branch added. Changing branch A does not invalidate the cache for branch B's diff. This is the primary cache efficiency win over sequential builds.

---

## Project structure

```
daeg/
├── Dockerfile              # builds the frontend image
├── main.go                 # BuildKit gRPC entrypoint
├── README.md
├── DESIGN.md               # design decisions and rationale
├── example/
│   └── Daegfile            # canonical polyglot + apt example
└── internal/
    ├── parser/
    │   ├── ast.go          # Stage, ResolveRule, ScriptResolver, DiscardResolver
    │   ├── args.go         # ARG substitution pre-processor
    │   └── parser.go       # 3-state machine parser
    ├── graph/
    │   └── dag.go          # topological sort, cycle detection, validation
    └── frontend/
        ├── build.go        # BuildKit frontend entrypoint
        └── solver.go       # AST → LLB, delegates instructions to Dockerfile2LLB
```

---

## Design

See [DESIGN.md](DESIGN.md) for the full record of decisions made during development, including alternatives considered and rejected.
