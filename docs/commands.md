# Skills 与 Slash Commands

## 调用方式

在聊天输入或 CLI `chat --message` 中输入 `/name args`。服务端统一解析：本地控制命令直接返回 `command.result`，自定义命令展开后进入正常 Agent run，权限、审批、checkpoint 和 model-io 不变。

```bash
./agent-core chat --message '/help'
./agent-core chat --message '/skill'
./agent-core chat --message '/compact 保留关键设计决策'
./agent-core chat --message '/snapshot create'
```

## 参考实现的 20 个内置命令

| 命令 | 用途 | 别名 |
|---|---|---|
| `/help [command]` | 命令帮助 | — |
| `/login [...]` | Coding Plan 登录 | — |
| `/logout` | 删除共享凭据 | — |
| `/compact [instructions]` | 持久化压缩当前会话 | — |
| `/init [notes]` | 创建或更新 AGENTS.md | — |
| `/expert [status|resume|stop|<task>]` | 专家审查与验证 Workflow | — |
| `/effort [list|<level>]` | 查看/切换推理档位 | `/variant` |
| `/dwf [list|cancel [runId]|resume <runId>]` | 管理动态 Workflow | — |
| `/fork [latest|checkpointId]` | 分叉会话历史 | — |
| `/locale [auto|en-US|zh-CN]` | 查看/切换 UI locale | `/language` |
| `/mcp [list|status|disconnect <server>]` | MCP 状态与断开 | — |
| `/plugins [list|enable|disable]` | 插件管理 | `/plugin` |
| `/mode [plan|build|edit|yolo|guarded|auto]` | 会话权限模式，显式 deny 和只读子 run 约束仍优先 | — |
| `/model [list|provider/model]` | 会话模型覆盖 | — |
| `/new` | 新会话 | `/clear` |
| `/resume [sessionId]` | 加载会话 | `/continue` |
| `/rewind [latest|checkpointId]` | 恢复快照 | — |
| `/skill [<name> [task]]` | 列出或强制加载 Skill | — |
| `/goal [pause|resume|clear|replace|<objective>]` | 会话目标 | `/target` |
| `/workflow <task>` | 设计声明式 DAG，用户确认后运行 | — |

AgentCore 支持其中 17 个及表内别名；`/login`、`/logout`、`/plugins` 属于账号或插件体系，当前返回明确的 unsupported capability，不会伪造成功。

额外管理命令：`/trust`、`/snapshot`、`/agents`、`/trace`、`/benchmark`。

## 自定义命令

在项目或用户目录创建：

```text
.zcode/commands/review.md
.agents/commands/review.md
.claude/commands/review.md
```

子目录形成冒号命名空间，例如 `.zcode/commands/frontend/review.md` 对应 `/frontend:review`。

```markdown
---
description: 审查当前变更
argument-hint: "[scope]"
skills: [code-review]
---

请审查 $1 的正确性、测试和可维护性。
附加范围：$ARGUMENTS
```

支持 `$ARGUMENTS`、`$1` 到 `$N` 和引号参数；无占位符时追加 `User arguments`。内置命令优先，自定义命令不能覆盖内置名称。

动态 `` !`command` `` 和 fenced `!` 块只在模板阶段改写为带来源的 hand-off note + fenced bash，**解析时不执行**。执行由模型通过普通 `run_terminal` 完成，仍受 Plan、审批、超时、进程组和 trace 约束。参数文本不会被识别为 shell 块；无法解析的插件变量返回 not-run 说明而不是空串。`@file` 必须走 workspace 文件服务，不允许借此访问目录外内容。

Bash 环境包含 `CLAUDE_CODE_SESSION_ID`、`CLAUDE_SESSION_ID`、`ZCODE_SESSION_ID`（conversation ID）与 `CLAUDE_PROJECT_DIR`、`ZCODE_PROJECT_DIR`（固定 workspace root），其余环境采用白名单，不继承宿主机 API key。

## Skills

Skills 是 `SKILL.md` 指令包，发现目录包括额外 roots、项目/用户的 `.zcode/skills`、`.agents/skills`、`.claude/skills`。项目目录按 Git worktree 由近及远查找，同名使用更高优先级并产生 diagnostics。

```markdown
---
name: code-review
description: 审查代码时使用，要求给出文件和行号证据。
---

先读取相关文件，再按严重性列出问题。
```

启动 prompt 只包含 name/description 等 metadata。模型需要时调用 `skill` 工具，正文最大 100 KiB，并返回 base directory。`allowed-tools`、`model`、`effort`、`context`、`hooks` 等风险字段需要当前 Workspace Trust，不能通过技能扩大权限。

## 相关 CLI

```bash
./agent-core skills
./agent-core skills load --name code-review
./agent-core workflows list
./agent-core workflows validate --file ./workflow.yaml
./agent-core workflows resume --id <run-id>
./agent-core snapshots list
./agent-core snapshots create
./agent-core snapshots restore --id <id> --allow-changes
./agent-core trust review
./agent-core trust grant --digest <bundle-digest>
```

完整 `skills.enabled/includeInstructions/metadataBudget/roots`、单文件覆盖与发现优先级示例见 [README SKILL 配置](../README.md#skill-配置)。默认预算 20000 字符；功能关闭后 API 返回 503。
