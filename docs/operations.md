# 部署、备份与运维

## 二进制与 Ubuntu

Go 版本以 `go.mod` 为准（当前 1.26.10），SQLite 驱动需要 CGO：

```bash
sudo apt update
sudo apt install -y build-essential gcc ca-certificates
go build -trimpath -ldflags='-s -w' -o agent-core ./cmd/agent-core
./agent-core config validate --config /opt/agent-core/agent-core.yaml
```

创建独立低权限账号，仅允许读取二进制/配置和写入 data/workspace/logs。不要使用 root 或 `www-data` 共享账号运行终端工具。

## systemd

仓库提供 [deploy/agent-core.service](../deploy/agent-core.service)。关键点：

- `EnvironmentFile=/opt/agent-core/.env`，文件权限 0600。
- 独立 `User=agent-core`。
- `NoNewPrivileges=true`、`PrivateTmp=true`、`ProtectSystem=strict`。
- 仅 `ReadWritePaths=/opt/agent-core/data /opt/agent-core/workspace /opt/agent-core/logs`。
- 生产配置 `terminal_enabled: false`；如果必须启用终端，额外评估这些限制是否与应用依赖兼容。
- `Restart=on-failure`，关闭时先取消 run，再等待 HTTP/后台任务和 MCP/进程清理。

```bash
sudo cp deploy/agent-core.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now agent-core
systemctl status agent-core
journalctl -u agent-core -f
```

不要同时把 `StandardOutput=append:` 和 journalctl 当成两份完整日志来源；选择一种并配置轮转。提供 [deploy/logrotate.conf](../deploy/logrotate.conf) 示例。

## Caddy

```caddyfile
example.com {
    handle_path /agent-core/* {
        reverse_proxy http://127.0.0.1:8900 {
            flush_interval -1
        }
    }
}
```

`handle_path` 去掉前缀，`/agent-core/v1/chat/stream` 映射到 `/v1/chat/stream`。SSE 必须禁用缓冲； heartbeat 每 15 秒发送。外层应同时配置 TLS、客户端速率限制、请求大小和可信代理策略。完整示例见 [deploy/Caddyfile.example](../deploy/Caddyfile.example)。

## Docker

```bash
docker build -t agent-core:local .
docker compose up -d
```

`deploy/container.yaml` 在容器内监听 `0.0.0.0` 并禁用终端，因此必须设置 `AUTH_TOKEN`。Compose 使用非 root、只读根文件系统、cap drop、no-new-privileges 和独立 data/workspace volume。不要把宿主机 `/`、Docker socket 或个人 `.ssh` 挂载进去。

## SQLite migration 与备份

启动时执行版本化 migration，账本包含版本、SHA-256 checksum、时间：

- 历史 migration 不可修改，checksum 不匹配会拒绝启动。
- 旧数据库有待执行 migration 时，先使用 SQLite `VACUUM INTO` 写入 `data/backups/agent-pre-migration-*.db`，兼容 WAL。
- 迁移在事务中执行，失败回滚。备份失败会阻止 migration，而不是无保护继续。
- 旧日志缺少的 tool 状态按 legacy 处理，不伪造数据。

手工备份应在服务账号下使用 SQLite backup/`VACUUM INTO`，不要只复制正在写入的主 db 文件。恢复前先停止服务、备份当前文件、验证备份可打开，再替换并执行 `config validate` 与最小对话。定期演练恢复，而不只检查文件存在。

## 健康、日志与指标

- `/health`: liveness，无需鉴权。
- `/ready`: DB/workspace/LLM 配置检查，需鉴权，失败 503。
- `/v1/metrics`: 请求数、5xx、平均时长、工具/MCP 数量。
- `server.log_format: json`: JSON 访问日志，包含 request ID/method/path/status/duration，不记录 query。

关联排障时保存 `request_id/conversation_id/run_id/tool_call_id`。doctor 输出不包含 API key；`config show` 也脱敏。

## 常见故障

- **启动拒绝非 loopback**：设置 AUTH_TOKEN 并禁用 terminal，或改回 127.0.0.1。
- **approval required**：同步 API 无审批客户端，改用 SSE 或调整经过评估的权限模式。
- **run resume 409**：有副作用工具中断，确认后选择 retry/skip；不要直接循环 retry。
- **SSE error 而非 done**：上游断流/解析失败不会保存为成功回答；检查 provider 日志与 run 事件。
- **MCP private address blocked**：只对明确可信的本机/私网 endpoint 设置 `allow_private: true`。
- **database migration checksum mismatch**：不要编辑旧 migration；恢复备份或新增正确迁移。
- **CLI 返回非零**：保留 stderr 和 run ID，检查是否审批超时、客户端断开或任务产物未生成。
