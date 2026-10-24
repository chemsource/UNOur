FROM golang:1.26-alpine3.24 AS build
RUN apk add --no-cache gcc musl-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY game ./game
COPY internal ./internal
RUN CGO_ENABLED=1 go build -buildvcs=false -trimpath -ldflags="-s -w" -o /out/uno-server ./cmd/server

FROM alpine:3.24
RUN apk add --no-cache ca-certificates sqlite \
    && addgroup -S -g 10001 unour \
    && adduser -S -D -H -u 10001 -G unour unour \
    && mkdir /data \
    && chown unour:unour /data
COPY --from=build /out/uno-server /usr/local/bin/uno-server
USER unour:unour
WORKDIR /data
ENV UNO_ADDR=:8088 UNO_DB_PATH=/data/unour.db
EXPOSE 8088
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:8088/healthz
ENTRYPOINT ["uno-server"]
