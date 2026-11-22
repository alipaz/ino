FROM golang:1.25.4-alpine3.22 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ ./cmd/api ./cmd/migrate ./cmd/read-projector

FROM alpine:3.22.2 AS runtime
RUN addgroup -S -g 10001 ino && adduser -S -D -H -u 10001 -G ino ino
USER ino

FROM runtime AS migrate
COPY --from=build /out/migrate /usr/local/bin/migrate
ENTRYPOINT ["/usr/local/bin/migrate"]
CMD ["up"]

FROM runtime AS read-projector
COPY --from=build /out/read-projector /usr/local/bin/read-projector
ENTRYPOINT ["/usr/local/bin/read-projector"]

FROM runtime AS api
COPY --from=build /out/api /usr/local/bin/api
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
