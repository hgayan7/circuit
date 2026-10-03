FROM golang:1.26.7-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/circuit ./cmd/circuit && \
    CGO_ENABLED=0 go build -trimpath -o /out/pilot-agent ./examples/pilot-agent

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

FROM runtime AS agent
COPY --from=build /out/pilot-agent /usr/local/bin/pilot-agent
ENTRYPOINT ["pilot-agent"]
