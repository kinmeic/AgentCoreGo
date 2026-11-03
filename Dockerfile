FROM golang:1.26.4-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -trimpath -ldflags="-s -w" -o /out/agent-core ./cmd/agent-core

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --system --gid 10001 agentcore \
    && useradd --system --uid 10001 --gid agentcore --home-dir /home/agentcore agentcore \
    && mkdir -p /data/agent /workspace /etc/agent-core /home/agentcore \
    && chown -R agentcore:agentcore /data /workspace /home/agentcore
COPY --from=build /out/agent-core /usr/local/bin/agent-core
COPY deploy/container.yaml /etc/agent-core/agent-core.yaml
USER 10001:10001
WORKDIR /home/agentcore
EXPOSE 8900
VOLUME ["/data", "/workspace"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD curl -fsS http://127.0.0.1:8900/health || exit 1
ENTRYPOINT ["/usr/local/bin/agent-core"]
CMD ["-config", "/etc/agent-core/agent-core.yaml"]
