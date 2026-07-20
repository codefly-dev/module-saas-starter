FROM golang:1.26.5-alpine3.23@sha256:622e56dbc11a8cfe87cafa2331e9a201877271cbff918af53d3be315f3da88cc AS builder

ARG TARGETARCH

WORKDIR /src

COPY code/go.mod code/go.sum ./
RUN go mod download

COPY code/ .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags='-w -s -extldflags "-static"' -o /out/migrate .

FROM alpine:3.23.5@sha256:fd791d74b68913cbb027c6546007b3f0d3bc45125f797758156952bc2d6daf40

WORKDIR /app

RUN apk add --no-cache ca-certificates && \
    addgroup -S migrate && adduser -S -G migrate -h /app migrate

COPY --chown=migrate:migrate --from=builder /out/migrate /usr/local/bin/migrate
COPY --chown=migrate:migrate migrations /app/migrations

USER migrate

ENTRYPOINT ["/usr/local/bin/migrate"]
