# syntax=docker/dockerfile:1

# ── build stage ────────────────────────────────────────────────────────────────
FROM golang:1.22-alpine AS build

WORKDIR /src

# Copy dependency manifests first so this layer caches independently of source.
# If go.mod and go.sum don't change, `go mod download` is not re-run.
COPY go.mod go.sum ./
RUN go mod download

# Now copy source and build a fully static binary.
# CGO_ENABLED=0 disables cgo so the binary has no libc dependency.
# -trimpath removes local filesystem paths from the binary (reproducibility).
# -ldflags="-s -w" strips debug symbols — reduces binary size significantly.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -ldflags="-s -w" \
        -o /daeg \
        .

# ── runtime stage ──────────────────────────────────────────────────────────────
# scratch is an empty image — just our binary, nothing else.
# This is correct here because:
#   1. The binary is statically linked (no libc needed)
#   2. BuildKit communicates over stdio, not a socket we need to set up
#   3. The frontend never executes user code — it only produces LLB
FROM scratch

COPY --from=build /daeg /daeg

# BuildKit invokes the frontend by running its ENTRYPOINT.
# It wires up stdin/stdout for the gRPC-over-stdio protocol automatically.
ENTRYPOINT ["/daeg"]
