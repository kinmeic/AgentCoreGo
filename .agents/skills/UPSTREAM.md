# ZCode Skills 来源与同步

这六个技能完整导入自 ZCode 源码，而不是从本机插件缓存复制。

- 上游提交：`a9fe401eede6e6e5e01be9048679c206fe6f02d9`
- 上游根目录：`apps/zcode-cli/packages`
- 导入范围：仅下表中的技能目录，不包含四个 Office viewer、插件 MCP 服务或 Desktop UI。

| 技能 | 上游插件 | 版本 | 上游目录（相对于 packages） |
| --- | --- | --- | --- |
| `docx` | `documents-plugin` | 0.2.1 | `documents-plugin/skills/docx` |
| `pdf` | `pdf-plugin` | 0.2.2 | `pdf-plugin/skills/pdf` |
| `pptx` | `presentations-plugin` | 0.2.1 | `presentations-plugin/skills/pptx` |
| `xlsx` | `spreadsheets-plugin` | 0.2.1 | `spreadsheets-plugin/skills/xlsx` |
| `visualize` | `visualize-plugin` | 0.1.1 | `visualize-plugin/skills/visualize` |
| `skill-creator` | `skill-creator-plugin` | 0.1.0 | `skill-creator-plugin/skills/skill-creator` |

## 本地兼容改动

- `docx`：Quick Setup 使用 `${ZCODE_SKILL_DIR}`，由 AgentCoreGo 的 `skill` 工具展开。
- `docx`、`pdf`、`pptx`、`xlsx`：最终交付使用普通 Markdown 绝对路径文件链接，不使用 ZCode 专用 citation directive。
- `visualize`：仅使用 standalone HTML 工作流；保留本地 renderer、样式和 vendor 资源，不依赖 Desktop Gen UI 或宿主 Agent 交互。
- `skill-creator`：保持上游内容不变。
- 所有上游许可证和 vendored 第三方许可声明均随原目录保留。四个 Office 技能的 frontmatter `license` 字段与随附 `LICENSE.txt` 存在上游表述差异，本次未擅自改写。

## 更新步骤

1. 确认上游提交和六个源目录无未提交改动，再完整同步对应目录，不能只更新 `SKILL.md`。
2. 重新应用上述 AgentCoreGo 兼容改动，并保留新增本地说明。
3. 更新本文件的提交与版本信息。
4. 运行 `go test ./internal/skills ./internal/tool`，再运行完整 `go test ./...`。
5. 通过 `./agent-core skills` 和 `./agent-core skills --name <name>` 检查实际发现与加载结果。
