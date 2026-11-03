# 项目配置、Trust、Subagents 与 Workflow

## 项目配置层级

有效配置按以下顺序合并：

```text
内置默认 → ~/.agent-core/config.yaml → 服务 YAML → 项目配置 → 环境变量
```

项目支持 `.agent-core/config.yaml`、`.zcode/config.json`、`zcode.json` 的可控子集：模型、effort、权限模式、UI、Skills、Commands 和 Workflow。项目不能覆盖监听地址、token、数据库、workspace root、密钥或安全 deny，也不能把权限升级为更高风险模式。每个 run 固定 RunSpec，不随运行中文件编辑改变。

## Workspace Trust

Trust 绑定 canonical workspace 和当前声明 bundle digest，存储在项目目录之外的 SQLite：

```text
status/review → 检查声明与 digest → grant → 可执行当前声明
声明改变 → digest 改变 → 必须重新授权
revoke → 删除当前 bundle 或全部授权
```

```bash
./agent-core trust review
./agent-core trust grant --digest <review返回的bundle_digest>
./agent-core trust revoke --digest <bundle-digest>
```

风险 Skill/Command、项目 Workflow、可执行 grader 等必须通过 Trust。损坏的 Trust 数据 fail closed，不自动覆盖。

## Subagents

模型可调用 `agent` 工具：

- `general-purpose`：独立子 Agent。
- `Explore`：继承父会话当前 mode 的只读子 Agent；恢复时也会重算父会话最新 mode，shell 命令按只读规则判定，危险命令仍需审批或拒绝。
- `run_in_background`：立即返回 run ID，之后查询状态。

子 run 具有独立消息、预算、trace/root/parent/depth 和 checkpoint。默认禁止递归 spawn，depth 上限防止无限扩张；取消按 exact run 传播，不误杀兄弟任务。子代理不能扩大父级授权，交互审批不可用时副作用工具失败关闭。

```http
GET /v1/agents?parent=<run-id>
POST /v1/agents/<run-id>/stop
```

## 声明式 Workflow

仅支持 YAML/JSON DAG，不执行任意 JavaScript。节点类型为 `agent` 和 `tool`，支持依赖、fan-out、join、`on_success/on_failure/always`、静态 items、重试、并发和预算。

```yaml
schema_version: 1
name: review-and-verify
nodes:
  - id: review
    type: agent
    prompt: "审查 {{item}}"
    items: ["模块 A", "模块 B"]
  - id: verify
    type: agent
    prompt: "验证审查结果：{{nodes.review.reply}}"
    depends_on: [review]
```

每次运行先保存 definition digest。节点记录 inputHash、attempt、状态和结果；恢复时复用匹配哈希的完成节点，失败/丢失节点按策略重试。并发、节点数、Agent 调用数和总时长都有硬限制。

`max_nodes` 同时限制 fan-out 后的任务总数；Agent 调用预算在 resume 时累计。`retry` 表示首次尝试之后最多重试的次数。工具节点保留 `args`，其中的字符串也支持 `{{item}}` 和 `{{nodes.<id>.result}}`；fan-out 的聚合结果按 item 下标以换行连接，单项可用 `{{nodes.<id>[0].result}}` 引用。

```bash
./agent-core workflows validate --file workflow.yaml
./agent-core workflows list
./agent-core workflows show --id <run-id>
./agent-core workflows resume --id <run-id>
./agent-core workflows cancel --id <run-id>
```

`/workflow` 只生成定义供用户确认，`/expert` 使用内置审查→验证 DAG，`/dwf` 管理运行。
