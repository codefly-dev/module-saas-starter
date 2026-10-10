FROM node:24.17.0-alpine3.23@sha256:7c70d1235c0b4c2bc9eeed5393d19f1bbdde6885ba0d58ba62bb385d7b0f3ff1 AS base

# Install dependencies
FROM base AS deps
# libc6-compat is a virtual name: apk resolves it to gcompat plus gcompat's own
# dependencies, so an `=<version>` pin would constrain one of the three packages
# actually installed and leave the rest floating. A base image digest pins the
# image, not the Alpine package index apk reads at build time, and that index
# carries only the current build of each package — so a version pin here does
# not go stale, it becomes unresolvable and fails the build outright.
# The resolved set is therefore RECORDED rather than pinned, and copied into
# both runtime images below: the OS packages an image was actually built from
# stay readable from the image whose digest the CLI captured.
RUN apk add --no-cache libc6-compat \
    && mkdir -p /codefly \
    && apk info -v | sort > /codefly/apk-packages.txt
WORKDIR /app

COPY code/package.json code/package-lock.json* ./
# npm workspaces are part of the install graph. Copy their complete package
# sources before npm ci so additive application packages behave the same in
# local CI and the production container build.
COPY code/packages ./packages
RUN npm ci

# Build
FROM base AS builder
WORKDIR /app
COPY code/ .
# Codefly applications may verify generated service dependencies during their
# build. Keep the service manifest available to that deterministic check without
# copying it into the final runtime image.
COPY service.codefly.yaml /service.codefly.yaml
# The Codefly archive builder may receive a service tree that already contains
# host node_modules. Remove it before restoring the clean, lock-derived install;
# otherwise host-absolute .bin symlinks can shadow the container executables.
RUN rm -rf node_modules
COPY --from=deps /app/node_modules ./node_modules
# npm nests a workspace's dependency under packages/<name>/node_modules when its
# version conflicts with one hoisted to the root. Those installs exist only in
# the deps stage (host copies are excluded from the context), so restore the
# workspace packages from there too, or the build resolves the hoisted version
# instead. The sources are the same context files the deps stage copied.
COPY --from=deps /app/packages ./packages

ENV NEXT_TELEMETRY_DISABLED=1

ARG FRONTEND_SKIN_RUNTIME
ENV FRONTEND_SKIN_RUNTIME=$FRONTEND_SKIN_RUNTIME

RUN mkdir -p public && npm run build


# SSR: standalone Node.js server
FROM base AS runner
WORKDIR /app

ENV NODE_ENV=production
ENV NEXT_TELEMETRY_DISABLED=1

RUN rm -rf /usr/local/lib/node_modules/npm \
      /usr/local/lib/node_modules/corepack \
      /opt/yarn-v1.22.22 && \
    rm -f /usr/local/bin/npm /usr/local/bin/npx \
      /usr/local/bin/corepack /usr/local/bin/yarn /usr/local/bin/yarnpkg

RUN addgroup --system --gid 1001 nodejs
RUN adduser --system --uid 1001 --ingroup nodejs nextjs

COPY --from=builder /app/public ./public

RUN mkdir .next
RUN chown nextjs:nodejs .next

COPY --from=builder --chown=nextjs:nodejs /app/.next/standalone ./
COPY --from=builder --chown=nextjs:nodejs /app/.next/static ./.next/static
COPY --from=deps /codefly/apk-packages.txt /codefly/apk-packages.txt

USER nextjs

EXPOSE 3000

ENV PORT=3000
ENV HOSTNAME="0.0.0.0"

CMD ["node", "server.js"]

