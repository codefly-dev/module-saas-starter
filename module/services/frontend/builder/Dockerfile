FROM node:24.17.0-alpine3.23@sha256:7c70d1235c0b4c2bc9eeed5393d19f1bbdde6885ba0d58ba62bb385d7b0f3ff1 AS base

# Install dependencies
FROM base AS deps
RUN apk add --no-cache libc6-compat
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

ENV NEXT_TELEMETRY_DISABLED=1

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

USER nextjs

EXPOSE 3000

ENV PORT=3000
ENV HOSTNAME="0.0.0.0"

CMD ["node", "server.js"]

