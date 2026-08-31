# Build stage
FROM golang:1.26.6-alpine3.23@sha256:e57c41c1d5864341031181b0db34b9a537bb5773eb6428e4e5bdaea0f9135406 AS builder

ARG TARGETARCH

# Install ca-certificates for HTTPS requests and git for private repos if needed
RUN apk add --no-cache ca-certificates git

# Set working directory
WORKDIR /app

# Copy the source code

COPY service.codefly.yaml service.codefly.yaml

COPY code code


# Download dependencies and build the binary
COPY code/go.mod code/go.sum code/
RUN cd code && go mod download
COPY code code
RUN cd code && \
    CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} go build -ldflags='-w -s -extldflags "-static"' -o /app/app .

# Final stage
FROM alpine:3.23.5@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40

# Runtime only needs the public CA bundle. Keep build tooling out of the image.
RUN apk add --no-cache ca-certificates

# Run from the Go source directory, mirroring the dev runner, so a source-dir
# relative runtime path (e.g. "../routing/rest") resolves to the same file it
# does in dev.
WORKDIR /app/code

# Set environment variables


# Create a non-root user
RUN adduser -D appuser

# Copy the binary from the builder stage
COPY --chown=appuser --from=builder /app/app .

# Ship the declared runtime asset, reproducing its source-tree location under
# /app so the source-dir relative path resolves the same as in dev.
COPY --chown=appuser routing /app/routing

# Use the non-root user
USER appuser

# Expose ports
EXPOSE 8080 9090

# Run the binary
CMD ["./app"]
