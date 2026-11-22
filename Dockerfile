FROM golang:1.25.4-alpine3.22 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

FROM alpine:3.22.2
RUN addgroup -S -g 10001 ino && adduser -S -D -H -u 10001 -G ino ino
COPY --from=build /out/api /usr/local/bin/api
USER ino
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/api"]
