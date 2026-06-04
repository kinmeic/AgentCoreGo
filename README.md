# Agent Core

轻量级 AI Agent 服务端，支持多轮对话、工具调用、MCP 集成和流式响应（SSE）。

## 特性

- **多轮对话** - 自动管理对话历史和上下文
- **工具系统** - 内置文件读写、终端执行、网页抓取等工具，支持 Host Callback 自定义工具
- **MCP 集成** - 通过 Model Context Protocol 动态接入外部工具服务
- **流式响应** - Server-Sent Events (SSE) 实时推送思考过程和工具调用状态
- **记忆系统** - 长期记忆存储与检索，支持向量化语义搜索
- **Workspace 沙箱** - 所有文件操作约束在 workspace 目录内，防止路径逃逸
- **SOUL.md** - 通过 workspace 内的 Markdown 文件定义 Agent 行为准则
- **上下文压缩** - 自动压缩过长对话，保留关键上下文

## 项目结构

```
AgentCore/
  cmd/agent-core/        # 入口程序
  internal/
    agent/               # Agent 编排循环（tool-use loop）
    compressor/          # 对话上下文压缩
    config/              # 配置加载与环境变量覆盖
    llm/                 # LLM 客户端（OpenAI 兼容）
    mcp/                 # MCP 服务端管理
    memory/              # 记忆服务
    model/               # 数据类型定义
    prompt/              # System Prompt 构建
    sandbox/             # Workspace 沙箱与路径验证
    server/              # HTTP 服务与 API 路由
    store/               # SQLite 持久化 + 向量存储
    tool/                # 工具注册表与内置工具
  agent-core.yaml        # 配置文件
```

## 快速开始

### 前置条件

- Go 1.22+
- CGO 支持（SQLite 驱动需要）

### 编译

```bash
git clone https://git.home.mrchen.net:8443/eugene/AgentCore.git
cd AgentCore
go build -o agent-core ./cmd/agent-core
```

### 配置

复制并编辑配置文件：

```bash
cp agent-core.yaml agent-core.yaml.bak
```

关键配置项：

```yaml
llm:
  base_url: "https://api.openai.com/v1"   # OpenAI 兼容的 API 地址
  api_key: "${LLM_API_KEY}"               # 建议使用环境变量
  model: "gpt-4o"

workspace:
  path: "./workspace"        # Agent 工作目录（沙箱）
  soul_file: "SOUL.md"       # Agent 行为准则文件

server:
  host: "0.0.0.0"
  port: 8900
  auth_token: "${AUTH_TOKEN}"  # 留空则不启用鉴权
```

### 环境变量

| 变量名 | 说明 |
|--------|------|
| `LLM_API_KEY` | LLM API 密钥 |
| `LLM_BASE_URL` | LLM API 地址 |
| `LLM_MODEL` | 模型名称 |
| `EMBEDDING_API_KEY` | Embedding API 密钥 |
| `AGENT_CORE_PORT` | 服务端口 |
| `AGENT_CORE_HOST` | 监听地址 |
| `AGENT_CORE_WORKSPACE` | Workspace 路径 |
| `AUTH_TOKEN` | 鉴权 Token |
| `DB_PATH` | 数据库路径 |
| `WORK_DIR` | 工具工作目录（兼容旧配置） |

### 运行

```bash
./agent-core -config agent-core.yaml
```

启动后会自动创建 `./workspace` 目录和默认 `SOUL.md` 文件。

## Ubuntu 服务器部署

### 1. 安装 Go

```bash
# 下载 Go（请根据实际版本调整 URL）
wget https://go.dev/dl/go1.22.5.linux-amd64.tar.gz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.22.5.linux-amd64.tar.gz

# 添加到 PATH
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.profile
source ~/.profile
```

### 2. 安装编译依赖

```bash
# sqlite3 驱动需要 CGO
sudo apt update
sudo apt install -y build-essential gcc
```

### 3. 部署应用

```bash
# 创建应用目录
sudo mkdir -p /opt/agent-core
sudo chown $USER:$USER /opt/agent-core

# 拉取代码并编译
cd /opt/agent-core
git clone https://git.home.mrchen.net:8443/eugene/AgentCore.git .
go build -o agent-core ./cmd/agent-core

# 创建数据和日志目录
mkdir -p data workspace logs
```

### 4. 配置环境变量

```bash
cat > /opt/agent-core/.env << 'EOF'
LLM_API_KEY=sk-your-api-key-here
LLM_BASE_URL=https://api.openai.com/v1
EMBEDDING_API_KEY=sk-your-embedding-key
AUTH_TOKEN=your-secure-auth-token
EOF
```

### 5. 创建 systemd 服务

```bash
sudo tee /etc/systemd/system/agent-core.service << 'EOF'
[Unit]
Description=Agent Core AI Service
After=network.target

[Service]
Type=simple
User=www-data
Group=www-data
WorkingDirectory=/opt/agent-core
EnvironmentFile=/opt/agent-core/.env
ExecStart=/opt/agent-core/agent-core -config /opt/agent-core/agent-core.yaml
Restart=on-failure
RestartSec=5
StandardOutput=append:/opt/agent-core/logs/stdout.log
StandardError=append:/opt/agent-core/logs/stderr.log

# 安全加固
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=/opt/agent-core/data /opt/agent-core/workspace /opt/agent-core/logs

[Install]
WantedBy=multi-user.target
EOF
```

```bash
# 设置目录权限
sudo chown -R www-data:www-data /opt/agent-core/data /opt/agent-core/workspace /opt/agent-core/logs

# 启动服务
sudo systemctl daemon-reload
sudo systemctl enable agent-core
sudo systemctl start agent-core

# 查看状态
sudo systemctl status agent-core
journalctl -u agent-core -f
```

### 6. 更新脚本

```bash
cd /opt/agent-core
git pull
go build -o agent-core ./cmd/agent-core
sudo systemctl restart agent-core
```

## Caddy 反向代理（二级目录）

将 Agent Core 代理到域名下的二级目录（如 `https://example.com/agent-core/`）。

### Caddyfile 配置

```caddyfile
example.com {
    # 代理 Agent Core 到 /agent-core/ 路径
    handle_path /agent-core/* {
        reverse_proxy http://127.0.0.1:8900
    }

    # 其他路由...
    handle {
        respond "Not Found" 404
    }
}
```

`handle_path` 会自动剥离 `/agent-core` 前缀后再转发到后端，即：
- 请求 `https://example.com/agent-core/v1/chat` 转发为 `http://127.0.0.1:8900/v1/chat`
- 请求 `https://example.com/agent-core/health` 转发为 `http://127.0.0.1:8900/health`

### SSE 流式响应支持

SSE 需要禁用缓冲。Caddy 默认会正确处理 SSE，但如果遇到问题，可以显式配置：

```caddyfile
example.com {
    handle_path /agent-core/* {
        reverse_proxy http://127.0.0.1:8900 {
            # 确保 SSE 不被缓冲
            flush_interval -1
        }
    }
}
```

### 完整示例（含静态文件和其他服务）

```caddyfile
example.com {
    # Agent Core API
    handle_path /agent-core/* {
        reverse_proxy http://127.0.0.1:8900 {
            flush_interval -1
        }
    }

    # 前端静态文件
    handle {
        root * /var/www/frontend
        file_server
        try_files {path} /index.html
    }
}
```

### 验证代理是否生效

```bash
# 直接访问后端
curl http://127.0.0.1:8900/health

# 通过 Caddy 代理访问
curl https://example.com/agent-core/health
```

返回类似以下内容即表示成功：

```json
{"mcp":0,"status":"ok","tools":6}
```

## API 参考

所有 API 请求需在 Header 中携带 `Authorization: Bearer <token>`（若启用了鉴权）。

### 对话

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/v1/chat` | 同步对话 |
| POST | `/v1/chat/stream` | 流式对话（SSE） |
| POST | `/v1/chat/{id}/stop` | 中断对话 |
| GET | `/v1/conversations` | 对话列表 |
| GET | `/v1/conversations/{id}` | 对话详情 |
| GET | `/v1/conversations/{id}/logs` | 对话日志 |
| PUT | `/v1/conversations/{id}` | 更新对话 |
| DELETE | `/v1/conversations/{id}` | 删除对话 |

### 记忆

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/v1/memories` | 记忆列表 |
| POST | `/v1/memories` | 创建记忆 |
| PUT | `/v1/memories/{id}` | 更新记忆 |
| DELETE | `/v1/memories/{id}` | 删除记忆 |

### 工具 & MCP

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/v1/tools` | 已注册工具列表 |
| POST | `/v1/tools/register` | 注册 Host Callback 工具 |
| DELETE | `/v1/tools/{name}` | 注销工具 |
| GET | `/v1/mcp` | MCP 服务列表 |
| POST | `/v1/mcp` | 连接 MCP 服务 |
| DELETE | `/v1/mcp/{name}` | 断开 MCP 服务 |

### 健康检查

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/health` | 健康检查（无需鉴权） |

### 示例：同步对话

```bash
curl -X POST http://127.0.0.1:8900/v1/chat \
  -H "Authorization: Bearer $AUTH_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "message": "帮我写一个 Python 快速排序函数",
    "source": "api"
  }'
```

### 示例：流式对话

```bash
curl -N -X POST http://127.0.0.1:8900/v1/chat/stream \
  -H "Authorization: Bearer $AUTH_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "message": "解释一下 Go 的 goroutine 调度模型"
  }'
```

响应为 SSE 事件流：

```
data: {"type":"thinking","iteration":1}

data: {"type":"tool_call","tools":["write_file"]}

data: {"type":"tool.started","tool":"write_file","preview":"..."}

data: {"type":"progress","summary":"Written 512 bytes to quicksort.py"}

data: {"type":"chunk","content":"我已经将快速排序函数写入"}

data: {"type":"done","reply":"完整回复...","usage":{"prompt_tokens":1200,"completion_tokens":350,"total_tokens":1550}}
```

## Workspace 与 SOUL.md

Agent 的所有文件操作被约束在 `workspace.path` 目录内（沙箱机制）。沙箱会：

- 阻止绝对路径逃逸
- 阻止 `../` 相对路径逃逸
- 通过 `EvalSymlinks` 阻止符号链接逃逸

`SOUL.md` 是位于 workspace 根目录的 Agent 行为准则文件，每次请求都会实时读取并注入 System Prompt。你可以编辑它来定义 Agent 的角色、行为规范和约束条件：

```markdown
# Agent Guidelines

## Identity
你是一个专注于 Python 开发的 AI 助手。

## Communication
- 使用中文回复
- 代码注释使用英文

## Constraints
- 所有代码必须包含类型注解
- 优先使用标准库
```

## License

MIT
