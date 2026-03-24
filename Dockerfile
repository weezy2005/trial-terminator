# ---- Stage 1: Builder ----
# Use the full Go image to compile the binary.
# We only need the Go toolchain during build — not in production.
FROM golang:1.22-alpine AS builder

WORKDIR /app

# Copy dependency files first — Docker caches each layer.
# If only source code changes (not go.mod/go.sum), Docker reuses the
# cached `go mod download` layer instead of re-downloading all dependencies.
# This makes rebuilds significantly faster.
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code.
COPY . .

# Build a statically linked binary.
# CGO_ENABLED=0: no C library dependencies — makes the binary portable.
# -ldflags="-w -s": strip debug symbols — reduces binary size by ~30%.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-w -s" -o bin/api ./cmd/api

# ---- Stage 2: Runner ----
# The final image contains ONLY the compiled binary.
# alpine:3.19 is ~7MB — compared to golang:1.22 which is ~600MB.
# Smaller images = faster deploys, smaller attack surface.
FROM alpine:3.19

# ca-certificates: needed for HTTPS outbound requests (e.g., calling external APIs).
RUN apk add --no-cache ca-certificates

WORKDIR /app

# Copy only the compiled binary from the builder stage.
# The Go source code, toolchain, and all intermediate files are discarded.
COPY --from=builder /app/bin/api .

EXPOSE 8080

CMD ["./api"]
