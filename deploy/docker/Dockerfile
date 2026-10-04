FROM golang:1.26.7-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/circuit ./cmd/circuit && \
    CGO_ENABLED=0 go build -trimpath -o /out/pilot-agent ./examples/pilot-agent && \
    CGO_ENABLED=0 go build -trimpath -o /out/operator-worker ./examples/operator-worker && \
    CGO_ENABLED=0 go build -trimpath -o /out/archive-worker ./examples/archive-worker

FROM alpine:3.22 AS runtime
RUN apk add --no-cache ca-certificates && \
    mkdir -p /var/lib/circuit && chown 10001:10001 /var/lib/circuit
USER 10001:10001
WORKDIR /var/lib/circuit

FROM runtime AS gateway
COPY --from=build /out/circuit /usr/local/bin/circuit
VOLUME /var/lib/circuit
EXPOSE 8443
ENTRYPOINT ["circuit"]

FROM alpine:3.22 AS boundary
RUN apk add --no-cache iptables
COPY deploy/docker/boundary.sh /usr/local/bin/circuit-boundary
ENTRYPOINT ["/bin/sh", "/usr/local/bin/circuit-boundary"]

FROM runtime AS agent
COPY --from=build /out/pilot-agent /usr/local/bin/pilot-agent
ENTRYPOINT ["pilot-agent"]

FROM runtime AS operations
USER root
RUN mkdir -p /backups /var/lib/circuit-ops && chown 10001:10001 /backups /var/lib/circuit-ops
USER 10001:10001
COPY --from=build /out/circuit /usr/local/bin/circuit
COPY --from=build /out/operator-worker /usr/local/bin/operator-worker
ENTRYPOINT ["operator-worker"]

FROM rclone/rclone:1.75.1@sha256:45401ad7410db1d67ffdb58e19059ad20b0d8e0285a60e38bbec55cc1019c7a5 AS storage-backends

FROM runtime AS archive
USER root
RUN mkdir -p /var/lib/archive && chown 10001:10001 /var/lib/archive
USER 10001:10001
COPY --from=build /out/archive-worker /usr/local/bin/archive-worker
COPY --from=storage-backends /usr/local/bin/rclone /usr/local/bin/rclone
ENTRYPOINT ["archive-worker"]
