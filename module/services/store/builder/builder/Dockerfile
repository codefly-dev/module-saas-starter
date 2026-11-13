# Compile on the builder's native platform; only the output targets TARGETARCH.
FROM --platform=$BUILDPLATFORM golang:1.27.4-alpine3.23@sha256:146e3b9c7c1e2ec3d3f5348c8a686151a75e1976e50efac9fc022ebe446a9a82 AS migrate-builder
ARG TARGETARCH
WORKDIR /src
COPY bootstrap/migrate/go.mod bootstrap/migrate/go.sum ./
# The managed store's migration runner builds beside migrate, from the same
# module graph and verified the same way: a managed render runs this image's
# /usr/local/bin/managed-bootstrap.
COPY bootstrap/migrate/cmd ./cmd
RUN set -eu; \
    printf '%s  go.mod\n%s  go.sum\n%s  cmd/managed-bootstrap/main.go\n' '3022abd58da2903a32e95ab4b3e4d2dac9b22093c10750199b328093ab974845' 'aab9d956a6aeb34ed88617cce260d9105831cde4f36970832c65e2d6e3524a9c' 'dfa85c33fc0ead2322fdf54492aea24b524a3f778262b61dd9b78cc2afc65f63' | sha256sum -c -; \
    architecture="${TARGETARCH:-$(go env GOARCH)}"; \
    case "$architecture" in amd64|arm64) ;; *) echo "unsupported target architecture: $architecture" >&2; exit 1 ;; esac; \
    CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" GOTOOLCHAIN=local \
    go build -mod=readonly -trimpath -tags postgres,file \
      -ldflags='-s -w -X main.Version=v4.19.1' \
      -o /migrate github.com/golang-migrate/migrate/v4/cmd/migrate; \
    CGO_ENABLED=0 GOOS=linux GOARCH="$architecture" GOTOOLCHAIN=local \
    go build -mod=readonly -trimpath -ldflags='-s -w' -o /managed-bootstrap ./cmd/managed-bootstrap
# The runtime contains neither Go nor module manifests: its SBOM describes the
# compiled binary, not the parent's unused module graph.
COPY bootstrap /bootstrap
RUN rm -rf /bootstrap/migrate

FROM alpine@sha256:bc21487101ae47172509130ee1092cc273a831968d64b617776d1f69c81e42ab

ARG TARGETARCH

# Literals, not build arguments: a label is provenance only if it reports what
# actually built the image, and a --build-arg must not be able to rewrite it.
LABEL dev.codefly.bootstrap.lock-digest="sha256:397d94277c885167677d6fc3a9badfc0e2af83463863b57a38593298a6097271" \
      dev.codefly.bootstrap.base="alpine@sha256:bc21487101ae47172509130ee1092cc273a831968d64b617776d1f69c81e42ab" \
      dev.codefly.bootstrap.base-version="3.21.10" \
      dev.codefly.bootstrap.apk-repositories="https://dl-cdn.alpinelinux.org/alpine/v3.21/main" \
      dev.codefly.bootstrap.apk-packages="postgresql17-client=17.12-r0" \
      dev.codefly.bootstrap.migrate-version="v4.19.1" \
      dev.codefly.bootstrap.migrate-toolchain="golang:1.27.4-alpine3.23@sha256:146e3b9c7c1e2ec3d3f5348c8a686151a75e1976e50efac9fc022ebe446a9a82" \
      dev.codefly.bootstrap.migrate-go-mod="3022abd58da2903a32e95ab4b3e4d2dac9b22093c10750199b328093ab974845" \
      dev.codefly.bootstrap.migrate-go-sum="aab9d956a6aeb34ed88617cce260d9105831cde4f36970832c65e2d6e3524a9c" \
      dev.codefly.bootstrap.migrate-drivers="postgres,file" \
      dev.codefly.bootstrap.managed-runner="dfa85c33fc0ead2322fdf54492aea24b524a3f778262b61dd9b78cc2afc65f63"

WORKDIR /app

# apk resolves from the locked repositories alone, and every package is pinned to an
# exact version: once upstream replaces one of them the build fails instead of
# quietly installing different bytes. .github/scripts/update-bootstrap-lock.sh
# moves the lock forward.
RUN printf '%s\n' "https://dl-cdn.alpinelinux.org/alpine/v3.21/main" >/etc/apk/repositories \
    && apk add --no-cache postgresql17-client=17.12-r0

COPY --from=migrate-builder /migrate /usr/local/bin/migrate
COPY --from=migrate-builder /managed-bootstrap /usr/local/bin/managed-bootstrap

# Only the staged bootstrap artifact and the runtime-access program enter the
# image. The build context is the whole service directory, which also holds
# local configuration and secrets, so nothing is copied wholesale. The staged
# tree carries only files matching the one migration-filename definition, so an
# editor backup left beside a migration never reaches the image to be pruned.
COPY --from=migrate-builder /bootstrap /app/bootstrap
COPY bootstrap.sql /app/bootstrap.sql
COPY runtime-access.sql /app/runtime-access.sql

# The bootstrap is ONE psql session over bootstrap.sql: it takes the
# runtime-access advisory lock, runs `migrate up` as a child process while still
# holding it, then includes runtime-access.sql for the grants. migrate used to
# run here as its own process, which left the two steps in different lock
# domains -- see bootstrap.sql for the collision that allowed between concurrent
# bootstrap pods. bootstrap.sh gates that session on readiness and on verifying
# the staged migration sources.
CMD ["/bin/sh", "/app/bootstrap/bootstrap.sh"]
