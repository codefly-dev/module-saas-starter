FROM alpine@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d

ARG TARGETARCH

# Literals, not build arguments: a label is provenance only if it reports what
# actually built the image, and a --build-arg must not be able to rewrite it.
LABEL dev.codefly.bootstrap.lock-digest="sha256:743f17dc4861c943d5263912882996d9726eba3d24cce8113e9b5308d05aa8d5" \
      dev.codefly.bootstrap.base="alpine@sha256:48b0309ca019d89d40f670aa1bc06e426dc0931948452e8491e3d65087abc07d" \
      dev.codefly.bootstrap.base-version="3.21.7" \
      dev.codefly.bootstrap.apk-repositories="https://dl-cdn.alpinelinux.org/alpine/v3.21/main" \
      dev.codefly.bootstrap.apk-packages="curl=8.14.1-r2 postgresql17-client=17.11-r0" \
      dev.codefly.bootstrap.migrate-version="v4.19.1" \
      dev.codefly.bootstrap.migrate-archives="amd64=2ac648fbd1b127b69ab5a7b33cf96212178f71e22379fc50573630c6f4c7ce18,arm64=2fea2455c0f3f07cc3f4b98471c951ad1a716059574b20b6416bd1e9058751c5"

WORKDIR /app

# apk resolves from the locked repositories alone, and every package is pinned to an
# exact version: once upstream replaces one of them the build fails instead of
# quietly installing different bytes. .github/scripts/update-bootstrap-lock.sh
# moves the lock forward.
RUN printf '%s\n' "https://dl-cdn.alpinelinux.org/alpine/v3.21/main" >/etc/apk/repositories \
    && apk add --no-cache curl=8.14.1-r2 postgresql17-client=17.11-r0

# The archive is downloaded to a file and checksum-verified against the lock before
# anything unpacks or runs it, so tampered release bytes never reach tar.
RUN set -eu; \
    architecture="${TARGETARCH:-$(apk --print-arch)}"; \
    case "${architecture}" in \
      x86_64) architecture=amd64 ;; \
      aarch64) architecture=arm64 ;; \
    esac; \
    case "${architecture}" in \
      amd64) checksum=2ac648fbd1b127b69ab5a7b33cf96212178f71e22379fc50573630c6f4c7ce18 ;; \
      arm64) checksum=2fea2455c0f3f07cc3f4b98471c951ad1a716059574b20b6416bd1e9058751c5 ;; \
      *) echo "unsupported target architecture: ${architecture}" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/migrate.tar.gz "https://github.com/golang-migrate/migrate/releases/download/v4.19.1/migrate.linux-${architecture}.tar.gz"; \
    printf '%s  %s\n' "${checksum}" /tmp/migrate.tar.gz | sha256sum -c -; \
    tar -xzf /tmp/migrate.tar.gz -C /tmp migrate; \
    install -m 0755 /tmp/migrate /usr/local/bin/migrate; \
    rm -f /tmp/migrate.tar.gz /tmp/migrate

# Only the staged bootstrap artifact and the runtime-access program enter the
# image. The build context is the whole service directory, which also holds
# local configuration and secrets, so nothing is copied wholesale. The staged
# tree carries only files matching the one migration-filename definition, so an
# editor backup left beside a migration never reaches the image to be pruned.
COPY bootstrap /app/bootstrap
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
