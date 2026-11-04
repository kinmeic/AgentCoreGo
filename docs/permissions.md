# 权限与安全策略

## 判定顺序

```text
工具存在且 toolset 启用
  → JSON Schema 参数校验
  → 静态 allow/deny/audit 策略（deny 永远优先）
  → approval mode / 会话授权
  → allow / ask / deny
  → 超时、取消、输出预算与进程清理
```

静态 deny 不会被 yolo 或用户审批绕过。`ask` 在非交互同步请求中没有审批客户端，因此失败关闭；需要交互时使用 `/v1/chat/stream`。

## 模式

| 模式 | 语义 |
|---|---|
| `plan` | 仅只读工具；写文件、终端、外部副作用拒绝。不是 OS 级只读沙箱 |
| `build` | 无副作用只读通常允许；workspace 或外部副作用需审批 |
| `edit` | 非破坏性 workspace 文件编辑通常允许；非文件编辑的副作用仍按 build 询问 |
| `guarded` | 与 ZCode 一致，普通模式判定放行；alwaysAsk 仍须询问 |
| `auto` | 保留模式，当前拒绝工具；不会自动授权 |
| `yolo` | 普通权限检查放行，适合已有容器/OS 隔离的无人值守任务；静态 deny 仍生效 |

未声明能力的外部 MCP/Host Callback 工具默认按高风险、外部副作用、需要审批处理。MCP annotations 只是不可信提示，不作为授权依据。

Explore 子 run 继承父会话当前 mode，同时保留独立、持久化的只读限制；`edit`、`yolo` 或会话授权都不能解除这项限制。

POSIX 终端仅对保守识别的字面量查询自动采用只读能力，例如 `ls -la`、`cat file.txt`、`rg -n TODO internal`。变量展开、命令替换、环境变量赋值、重定向、换行和未知选项均保留普通终端权限要求。Shell 中的 Git、find、sed、awk、分页器等命令也保留普通终端权限要求，因为它们可能写文件或运行其他程序；只读 Git 查询应使用 `git_readonly`。Windows 的 `cmd.exe` 不使用 POSIX 分类器。

## 审批生命周期

1. 工具到达 `ask`，生成绑定 conversation/run/tool-call 的 `request_id`。
2. run 进入 `waiting_approval`，SSE 发出 `approval.required`，参数预览递归脱敏且最长 500 rune。
3. 客户端在 5 分钟内提交：

   ```json
   {"decision":"allow","scope":"once"}
   ```

   `scope=conversation` 会对该会话的同名工具持续授权；`once` 仅当前调用。
4. 重复响应、过期请求和未知 ID 返回 404；取消和超时使工具失败。审批事件无法持久化时立即失败，不会无意义等待。
5. run 恢复 `running`，工具结果作为成功或错误反馈给模型。

会话授权保存在进程内存中，重启后不恢复；待审批 run 需要显式 resume，副作用工具仍需 retry/skip 策略。

## 工具能力

Provider 可声明：

- `read_only`, `destructive`, `concurrent_safe`
- `side_effect_scope`: none/workspace/memory/network/external/system/interaction
- `risk_level`: low/medium/high
- `needs_approval`
- `timeout`, `max_output_bytes`

这些声明服务权限与资源治理，不替代操作系统隔离。

## 显式工具规则

```yaml
permission:
  mode: build
  allowedTools: [write_file]
  disallowedTools: [run_terminal]
```

工具名以 `/v1/tools` 为准。显式 deny 在 yolo/guarded 仍生效，这是独立服务保留的安全边界；与 ZCode 部分普通工具的判定顺序存在差异。Explore 的只读约束同样不可被允许规则解除。`always_ask` 强制确认独立于普通 needs_approval 提示。

旧 `security.policies` YAML 已删除，不能继续使用。字符串工具规则不替代 OS 隔离。

Guarded 的终端规则保留服务端保守边界：无法证明只读的本地 shell 命令每次询问，不能用会话授权跳过。参考 ZCode 使用更细的危险 Bash 语法匹配器；Go 服务不会把未实现的匹配器当作已具备能力。普通非终端工具仍按 guarded 语义处理。

## 终端逐次审批与 sudo

```yaml
permission:
  mode: build
  alwaysAskTerminal: true
```

`alwaysAskTerminal` 默认为 `false`。开启后，`build`、`edit`、`guarded`、`yolo` 中每个无法保守判定为只读的 `run_terminal` 调用都必须逐次审批。`allowedTools`、`autoApproveHighRisk` 和 `scope=conversation` 会话授权都不能跳过；即使客户端选择“整个会话允许”，下一次非只读终端调用仍会询问。CLI 会在审批问题前显示事件已有的脱敏参数 preview。

该开关不会把 deny 提升为 ask：`plan`、`auto`、只读 run 和 `disallowedTools` 仍按原规则拒绝。只读终端豁免保持不变；`auto` 本身仍拒绝工具。开启此选项不会授予 OS 权限，也不能替代终端隔离。

仅在确需特权运维时配置免密 sudo：主服务仍必须以低权限 `agent-core` 账号运行，要求 `sudo -n` 和 `alwaysAskTerminal: true`，并优先使用固定命令、固定参数的窄 sudoers 规则。可选 drop-in 会主动削弱 systemd 隔离，见[部署文档](operations.md#可选免密-sudo高风险)与 [sudoers 示例](../deploy/agent-core.sudoers.example)。
