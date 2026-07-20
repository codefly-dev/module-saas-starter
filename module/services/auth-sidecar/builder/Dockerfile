# The sidecar imports generated account clients through a local Go replace.
# Codefly and CI provide this named BuildKit context from
# module/services/accounts/code; P1-PROTO-004 removes the cross-service source
# dependency once clients are generated from the versioned contract.
FROM scratch AS accounts-code

# Build stage
FROM golang:1.26.5-alpine3.23@sha256:622e56dbc11a8cfe87cafa2331e9a201877271cbff918af53d3be315f3da88cc AS builder

ARG TARGETARCH

# Install ca-certificates for HTTPS requests and git for private repos if needed
RUN apk add --no-cache ca-certificates git

# Set working directory
WORKDIR /app

# Copy the source code

COPY service.codefly.yaml service.codefly.yaml

COPY code code

COPY --from=accounts-code / /accounts/code


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
