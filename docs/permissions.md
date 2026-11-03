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
| `build` / `smart` | 只读通常允许；声明需要审批、破坏性、高风险工具需要审批 |
| `edit` | 非破坏性 workspace 文件编辑通常允许；外部副作用仍需审批 |
| `guarded` | 普通工具较宽松，高风险、破坏性或声明审批的工具逐次询问 |
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

## 自定义策略

```yaml
security:
  policies:
    - name: deny-sudo
      effect: deny
      resource: terminal
      conditions:
        - field: target
          operator: regex
          values: ['(^|\\s)sudo(\\s|$)']

    - name: audit-sensitive-write
      effect: audit
      resource: file
      conditions:
        - field: target
          operator: contains
          values: ['.env', 'id_rsa']
```

字段支持 `target/operation/resource` 及 run metadata。策略日志只记录资源、操作和 conversation，不记录完整命令、路径或密钥。

> 字符串策略和 regex 不是 shell 解析器，不能阻止所有等价命令。需要强隔离时禁用终端，并使用容器、独立低权限用户、只读根文件系统和资源限额。
