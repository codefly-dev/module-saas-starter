FROM golang:alpine AS builder

# Copy go mod and sum files to cache download
WORKDIR /app/code
COPY code/go.mod code/go.sum ./

RUN go mod download

WORKDIR /app

COPY service.codefly.yaml service.codefly.yaml

COPY code code


WORKDIR /app/code
# Build the binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags '-s -w' -o app

FROM alpine:latest
WORKDIR /app

# Set up a non-root user
RUN adduser -D appuser
USER appuser
COPY --chown=appuser service.codefly.yaml .
COPY --chown=appuser --from=builder /app/code/app .
EXPOSE 8080
EXPOSE 9090



CMD ["./app"]
