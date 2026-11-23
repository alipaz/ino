FROM golang:1.25.4-alpine3.22 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/api ./cmd/migrate ./cmd/seed ./cmd/cache-sync-worker

FROM alpine:3.22.2 AS runtime
RUN addgroup -S -g 10001 ino && adduser -S -D -H -u 10001 -G ino ino
USER ino

FROM runtime AS migrate
COPY --from=build /out/migrate /usr/local/bin/migrate
ENTRYPOINT ["/usr/local/bin/migrate"]
CMD ["up"]

FROM runtime AS seed
COPY --from=build /out/seed /usr/local/bin/seed
ENTRYPOINT ["/usr/local/bin/seed"]

FROM runtime AS cache-sync-worker
COPY --from=build /out/cache-sync-worker /usr/local/bin/cache-sync-worker
ENTRYPOINT ["/usr/local/bin/cache-sync-worker"]

FROM runtime AS api
COPY --from=build /out/api /usr/local/bin/api
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
