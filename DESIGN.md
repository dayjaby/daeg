# Daeg — Design Document

This document records the design decisions made during development, including the reasoning behind them and the alternatives that were considered and rejected.

---

## Name

**Daeg** (also spelled *dagaz*) is the Elder Futhark rune ᛞ, meaning "day" or "dawn." It visually resembles two triangles mirrored — a loose nod to the DAG (Directed Acyclic Graph) structure at the heart of the tool. The build file is called a `Daegfile`.

---

## Motivation

Docker's overlay filesystem is a linked list of layers:

```
Base → Layer A → Layer B → Layer C
```

BuildKit already models builds internally as a DAG and executes independent stages in parallel. But the Dockerfile syntax forces a linear mental model — `FROM` accepts exactly one parent, so users cannot express the true dependency structure of their builds.

The consequences are practical:

- **Cache invalidation is contagious.** One change in a shared dependency invalidates everything downstream, even unrelated branches.
- **Deduplication is implicit.** Two images that share a Python base each carry their own copy of every layer rather than referencing a shared node.
- **Cleanup is repeated.** Every branch that runs `apt-get` must also clean up `/var/lib/apt/lists` — you cannot pay that cost once at the merge point.

Daeg makes the DAG explicit and user-controlled.

---

## Architecture: BuildKit frontend

Daeg is implemented as a **BuildKit custom frontend**. When the first line of a Daegfile contains:

```
# syntax=ghcr.io/dayjaby/daeg:latest
```

BuildKit pulls that image and runs it as a gRPC server. The frontend receives the raw file bytes and returns LLB (a protobuf-encoded DAG of build operations). BuildKit then executes those operations and produces an OCI image.

This architecture gives us for free: layer caching, OCI output, registry push/pull, secret handling, SSH forwarding, multi-platform builds, and cache mounts.

**Alternative considered:** A standalone CLI that shells out to `docker build`. Rejected because it cannot produce real shared layer nodes — it can only produce flat images that happen to have the same bytes.

---

## Language: Go

Go was chosen because BuildKit and all its libraries are written in Go. The LLB client SDK (`github.com/moby/buildkit/client/llb`) is Go-native and there is no equivalent for other languages.

---

## Instruction delegation

Daeg does **not** reimplement Dockerfile instructions. `RUN`, `COPY`, `ADD`, `HEALTHCHECK`, `ENTRYPOINT`, `CMD`, `LABEL`, `EXPOSE`, `USER`, `STOPSIGNAL` are all delegated to BuildKit's own `dockerfile2llb.Dockerfile2LLB` converter.

For each stage, the solver synthesizes a minimal Dockerfile from the stage's raw instruction lines and calls `Dockerfile2LLB` with the resolved base state injected as `MainContext`. This means Daeg inherits all of BuildKit's instruction semantics, shell quoting rules, heredocs, bind mounts, and future improvements automatically.

The `applyRawLines` method in the solver exists only as a fallback for unit testing without a live BuildKit daemon.

**Alternative considered:** Reimplementing instructions in LLB directly. Rejected — fragile, incomplete, and duplicates a large body of well-tested work.

---

## Instruction delegation for merged stages

After computing the merged `llb.State`, the solver injects it as `MainContext` into `Dockerfile2LLB` with a synthetic `FROM scratch` Dockerfile. The instructions then run on top of the merged state exactly as they would on any other base.

`COPY --from=<stage>` and `FROM <stage>` both work with merged stages because every resolved stage — whether simple or merged — is stored in the solver's `resolved` map and passed to `Dockerfile2LLB` as a named context via the `input:` scheme. BuildKit's own resolution logic looks them up transparently.

---

## Merge semantics: Diff + Merge

BuildKit's `llb.Merge` is designed to be used with `llb.Diff`:

```go
base := llb.Image("ubuntu:24.04")
branchA := base.Run(...).Root()
branchB := base.Run(...).Root()

diffA := llb.Diff(base, branchA) // only what A added
diffB := llb.Diff(base, branchB) // only what B added

merged := llb.Merge([]llb.State{base, diffA, diffB})
```

Using `Diff` isolates what each branch added relative to the common base. This means changing branch A does not invalidate the cache for branch B's diff. This is the key cache efficiency win over sequential Dockerfile builds.

When no common base exists (parents come from different image refs), the solver falls back to a direct `llb.Merge` of the full states, which is correct but loses some cache independence.

---

## Default conflict behaviour: error

When two branches both modify the same file with different content, Daeg fails the build by default. This is analogous to Rust's borrow checker or Go's unused import error — make the implicit explicit.

**Identical bytes are not a conflict.** If two branches both install `libc6` and `apt` resolves to the same version, the content hashes match and the merge succeeds silently. This is the common case for shared `apt` dependencies.

**Different filenames in the same directory are not a conflict.** If branch A writes `/etc/ld.so.conf.d/cuda.conf` and branch B writes `/etc/ld.so.conf.d/opencv.conf`, both files are kept. Only files with the same path and different content are conflicts.

---

## RESOLVE directives

Three forms, all scoped to a merge header (before `AS`):

### Glob-scoped strategy

```
RESOLVE /etc/ld.so.conf.d/* STRATEGY union
RESOLVE /usr/bin/*           STRATEGY priority
```

Scopes a conflict resolution strategy to a path glob. Strategies:

- `union` — overlay both parents, last parent wins on conflict
- `priority` — left-most parent wins
- `error` — explicit fail (same as default, but self-documenting intent)

### Script resolver

```
RESOLVE WITH script ldconfig
RESOLVE WITH script update-ca-certificates --fresh
```

Runs a command inside the merged container after glob rules are applied. Used for commands like `ldconfig` that reconstruct derived files (shared library caches, certificate indexes) from their inputs. Scripts run in declaration order.

No quotes are required — everything after `script ` to the end of the line is the command. This allows commands with arguments without any escaping.

**Alternative considered:** Scoping script resolvers to a glob (e.g. `RESOLVE /etc/ld.so.cache STRATEGY script "ldconfig"`). Rejected because `ldconfig` does not care which specific file conflicted — it repairs the cache unconditionally. A global repair step is the right model.

### Discard resolver

```
RESOLVE WITH discard /var/lib/apt/lists /var/cache/apt /var/log /tmp
```

Removes paths from the merged layer before sealing it. Replaces the per-branch `&& rm -rf ...` idiom — pay the cleanup cost once at the merge point rather than in every branch. Paths must be absolute.

Discards run after all script resolvers, as the final step before sealing the layer.

**Execution order inside a merge stage:**
1. Apply filesystem merge across all parents
2. Check every conflict against glob RESOLVE rules (error if unmatched)
3. Run RESOLVE WITH script commands in declaration order
4. Apply RESOLVE WITH discard paths
5. Seal the layer
6. Execute instructions (RUN, COPY, ENV, ...)

---

## ARG substitution

ARG declarations are processed before parsing, not during parsing. The `parser.SubstituteArgs` function collects all `ARG` declarations and their defaults, applies `--build-arg` overrides, and substitutes `${VAR}` and `$VAR` occurrences throughout the source text before `parser.Parse` is called.

This allows ARG to be used in MERGE parent lists:

```dockerfile
ARG PYTHON_STAGE=python-app
ARG NODE_STAGE=node-app

FROM MERGE(${PYTHON_STAGE}, ${NODE_STAGE})
AS final
```

`--build-arg` values arrive from BuildKit as `build-arg:<KEY>=<VALUE>` entries in `BuildOpts.Opts`.

---

## Syntax: no quotes for script commands, no continuation characters

Script commands are unquoted — everything after `script ` to end of line is the command:

```
RESOLVE WITH script update-ca-certificates --fresh
```

This avoids shell-style quoting complexity. Since there is nothing after the command on the line, there is no ambiguity.

Merge headers use free newlines rather than line continuation characters:

```dockerfile
FROM MERGE(a, b, c)
RESOLVE WITH script ldconfig
RESOLVE WITH discard /var/lib/apt/lists /tmp
AS combined
```

The parser enters `merge_header` mode after `FROM MERGE(...)` and exits it when it sees `AS`. No backslashes or indentation rules are needed.

---

## Parser state machine

Three states:

| State | Entered by | Exits to |
|---|---|---|
| `normal` | start of file, or after completing a stage | `merge_header` on `FROM MERGE(...)`, `instructions` on `FROM <image>` |
| `merge_header` | `FROM MERGE(...)` | `instructions` on `AS <name>` |
| `instructions` | `AS <name>` | `normal` on next `FROM` |

`RESOLVE` after `AS` is a parse error. `ARG` in `normal` state is silently skipped (consumed by `SubstituteArgs`).

---

## DAG validation

The validator catches all errors before any LLB is constructed:

- **Duplicate stage names** — ambiguous references
- **Self-references** — `FROM MERGE(a, a) AS a`
- **Missing stage references** — parent names that are neither a defined stage nor an image ref
- **Cycles** — detected via three-colour DFS

Unreachable stages are **not warned about**. Tools like `docker buildx build --target <stage>` make it common to define stages that are not reachable from the final stage. A warning here would be noise.

All errors are collected before returning, so a Daegfile with three problems reports all three rather than stopping at the first.

---

## `--target` support

The solver respects the `--target` flag via `BuildOpts.Opts["target"]`. Merged stages are valid targets — `docker buildx build --target system-deps .` works regardless of whether `system-deps` was produced by a `FROM` or a `FROM MERGE(...)`.

---

## What Daeg does not do

- **Per-file conflict detection at build time.** The `error` strategy is a declaration of intent enforced structurally; actual byte-level conflict detection across layer diffs is delegated to BuildKit's merge implementation.
- **ARG variable substitution inside instruction lines.** `SubstituteArgs` substitutes ARG values in MERGE parent lists and FROM image names, which is the critical path. Full substitution inside `RUN` commands is handled by `Dockerfile2LLB` in the primary execution path.
- **Multi-platform builds.** The solver currently marshals LLB for `linux/amd64`. Platform-aware solving requires threading `ocispecs.Platform` through the solver and calling `Dockerfile2LLB` per platform.
