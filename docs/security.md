# 安全模型

## 定位与信任边界

AgentCore 当前是**单租户、单进程、单 workspace** 服务。静态 Bearer token 是 API 访问控制，不提供用户、角色、租户或资源属主隔离。所有会话共享记忆、工具注册表和 MCP/Host 配置；不要将它直接当成多用户 SaaS。

模型、MCP 服务、Host Callback、workspace 内容和终端命令都可能是不可信输入。权限提示不是用户已经安全审查命令的保证，调用方仍需检查参数与产物。

## 网络暴露

非 loopback 监听启动校验要求：

1. 配置 `AUTH_TOKEN`。
2. `server.terminalEnabled: false`。
3. 生产环境在反向代理启用 TLS、外层限流和日志脱敏。

Bearer 比较使用常量时间。`/health` 是公开 liveness；`/ready`、指标和管理 API 需要鉴权。CORS 使用精确 origin，不支持 `*`。

## Workspace 与文件

文件工具执行 cleaned path、workspace 边界、已有符号链接和“尚不存在目录的最近祖先”检查。写入采用：

1. 读取文件并计算 SHA-256 revision。
2. 要求 `expected_revision`，检测覆盖前外部修改。
3. 同目录临时文件、权限保留、写入与 fsync。
4. 再次检查 revision/symlink，原子 rename，并尽力同步目录。

并发 AgentCore 写入按路径互斥；外部进程在最终 rename 前修改仍存在操作系统 TOCTOU 窗口，因此 revision 是强防护而非分布式事务。不要把不可信用户映射到同一 OS 服务账号。

读取最大 2 MiB，工具输出另有上限。删除/移动需要 revision，目标不能覆盖既有文件。Glob/grep 不跟随目录 symlink，限制条目、文件大小和结果数。

## 终端不是沙箱

`working_dir` 只限制初始目录，shell 可以 `cd`、访问绝对路径、网络和当前 OS 用户的全部权限。黑名单无法覆盖所有等价命令。

生产建议：

- 默认禁用终端。
- 必须启用时，用独立低权限用户/容器，只读根文件系统，单独可写 workspace，cap drop、no-new-privileges、CPU/内存/PID/磁盘限额。
- Unix 按进程组清理，Windows 使用 `taskkill /T`；仍应在外层运行器设置硬超时和资源限制。
- 不使用宿主机 Docker socket 或挂载敏感目录。

## MCP / Web / Host Callback

默认阻断 loopback、私网、link-local、metadata 和非 HTTP 协议，DNS 解析与 dial 时再次检查。`allow_private` 是逐服务显式例外。跨源重定向被拒绝，认证 header 不转发到其他 origin。

MCP 工具 annotations 不作为可信权限依据；外部工具默认高风险。stdio 使用参数数组、环境白名单和进程组清理。

Host Callback 使用独立 HMAC secret、timestamp、nonce 和响应上限。Host 必须检查时间窗口并缓存 nonce 防重放。数据库中保存 secret 和 MCP 配置，文件权限与备份同等敏感。

## 数据与日志

- SQLite 包含消息、工具参数、记忆、MCP/Host 凭据；只允许服务账号读取。
- 启动 migration 前使用 `VACUUM INTO` 备份到 `data/backups`，正确处理 WAL。
- 日志字段中 token/secret/api key/authorization/password 自动脱敏；审批参数预览递归脱敏。
- 策略日志不记录完整命令/路径；HTTP 日志不记录 query string。
- 不把生产消息正文、API key、数据库或 `.env` 上传到第三方诊断服务。
- 备份与导出文件包含相同敏感信息，应加密、限制访问并设置保留周期。

## 恢复安全

工具结果和 checkpoint 原子提交。已完成工具不会重放；中断时有副作用的工具必须显式 retry/skip。不要把恢复当成幂等保证，外部 Host/MCP 工具仍应自行实现 idempotency key 或去重。
