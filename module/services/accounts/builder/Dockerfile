# Build stage
FROM golang:1.27.1-alpine3.23@sha256:d9e2f2f07b10cc922da3e80e035c3058810b328d5aef82d2c63680967c5e2ec9 AS builder

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
FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b

# Runtime only needs the public CA bundle. Keep build tooling out of the image.
RUN apk add --no-cache ca-certificates

# Set working directory
WORKDIR /app

# Set environment variables


# Create a non-root user
RUN adduser -D appuser

# Copy the binary from the builder stage
COPY --chown=appuser --from=builder /app/app .

# Use the non-root user
USER appuser

# Expose ports
EXPOSE 8080 9090

# Run the binary
CMD ["./app"]
