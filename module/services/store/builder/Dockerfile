FROM alpine:3.21

WORKDIR /app

RUN apk add --no-cache curl postgresql17-client
RUN curl -L https://github.com/golang-migrate/migrate/releases/download/v4.15.0/migrate.linux-amd64.tar.gz | tar xvz
RUN mv migrate /usr/local/bin/migrate

COPY . .
COPY migrations /app/migrations
COPY builder/runtime-access.sql /app/runtime-access.sql

CMD set -eu; \
    /usr/local/bin/migrate -path /app/migrations -database "${CODEFLY_POSTGRES_MIGRATION_CONNECTION}" up; \
    psql "${CODEFLY_POSTGRES_MIGRATION_CONNECTION}" --no-psqlrc --set=ON_ERROR_STOP=1 --file=/app/runtime-access.sql
