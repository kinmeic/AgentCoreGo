# GitHub Actions 发版

[Release 下载](https://github.com/kinmeic/AgentCoreGo/releases)按操作系统和架构选择。Windows 使用 ZIP，其余便携包使用 tar.gz。压缩包内有可执行文件、配置示例和许可证。

| 平台 | 架构 | 产物 |
|---|---|---|
| Linux | x86_64、ARM64 | 静态 musl tar.gz |
| macOS | Intel、Apple Silicon | tar.gz |
| Windows | x86_64 | 便携 ZIP |
| OpenWrt | x86_64、aarch64_cortex-a53 | 静态 tar.gz、opkg .ipk、OpenWrt 25.12.5 SDK .apk |

所有构建包含 SQLite 和 FTS5。发布工作流逐个运行可执行文件，核对版本并启动服务，确认 SQLite 初始化成功后才打包。OpenWrt ARM64 在 Cortex-A53 模拟器中运行这一检查。

使用 `SHA256SUMS` 核对下载文件，也可验证构建来源。

```bash
gh attestation verify agent-core-v0.4.0-linux-x86_64.tar.gz --repo kinmeic/AgentCoreGo
```

## 触发发布

在 GitHub Actions 的 **release** 工作流选择 `main`，填写 `release_tag`，例如 `v0.4.0`。也可以使用 CLI。

```bash
gh workflow run release.yml --ref main -f release_tag=v0.4.0
```

全部测试、构建和资产检查通过后，工作流为该次构建的提交创建 Git tag 并发布 Release。手动运行时留空 `release_tag` 只生成 Actions artifacts。推送已创建的 `vMAJOR.MINOR.PATCH` 标签也会触发发布。可执行文件报告标签中的版本。

仓库配置 `MINISIGN_SECRET_KEY`（以及加密密钥对应的 `MINISIGN_PASSWORD`）后，工作流另外生成 `SHA256SUMS.minisig`。GitHub 构建证明和 SHA256 校验清单由发布工作流生成。

OpenWrt 安装后，示例配置位于 `/etc/agent-core/agent-core.yaml.example`，按设备选择可写的工作区和数据库路径，再使用 `agent-core -config <配置路径>` 启动。
