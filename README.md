# test-agent

一个运行于测试机上的轻量级远程命令执行 Agent，提供 HTTP RESTful API，用于替代 SSH 交互，让主控端可以远程下发命令、运行脚本、上传文件并收集执行结果。

[English README](README_EN.md)

## 功能特性

- **远程命令执行**：同步执行（`POST /exec`）与异步任务（`POST /tasks`）两种模式
- **文件上传**：通过 `POST /api/v1/upload` 直接下发测试程序或脚本
- **动态 Token 鉴权**：Token 每日自动轮换，写入受权限保护的文件，避免硬编码
- **安全过滤**：命令黑名单/白名单、路径穿越防护、文件大小限制、扩展名白名单
- **资源限制**：命令超时控制、输出截断（默认 10MB）、最大上传限制（默认 64MB）
- **进程管理**：Unix 下使用进程组（PGID）清理，超时或取消时 kill 整个进程树；Windows 下直接结束主进程
- **优雅退出**：监听 SIGTERM/SIGINT，等待正在运行的任务结束后再关闭 HTTP 服务
- **防篡改日志**：结构化日志本地落盘，Ed25519 签名 + SHA256 哈希链 + 全局序列号，支持 MQ 异步复制与离线校验
- **密钥对管理**：内置命令生成签名密钥对，并支持对已落盘日志进行完整性校验

## 使用场景

### 作为 AI 编程助手的远程测试机

将 `test-agent` 部署到一台测试机后，它可以作为 **Claude Code 等 AI 编程工具的远程执行环境**，完全替代手动登录服务器操作：

- **无需暴露 SSH**：主控端通过 HTTP API + 动态 Token 下发命令，不需要把 SSH 私钥或账户密码交给任何工具。
- **即装即用**：只需在目标机器上启动 Agent，即可从主控端远程执行命令、上传脚本、收集输出。
- **完整执行记录**：所有命令执行结果、耗时、输出内容均通过 `log/slog` 以 JSON 形式落盘，方便审计、回溯和排障。
- **安全可控**：命令黑白名单、超时控制、输出限制、路径穿越防护等多重机制，避免误操作或恶意指令影响生产环境。

### 持续集成与回归测试

- 在 CI/CD 流水线中调用 `/api/v1/exec` 或 `/api/v1/tasks` 远程运行测试脚本。
- 通过 `/api/v1/upload` 下发待测二进制或测试包，执行完成后收集结果。
- 异步任务模式适合长时间运行的回归测试，主控端可轮询或回调获取结果。

## 技术栈

- Go 1.22+（标准库 + `gopkg.in/yaml.v3`，MQ 可选依赖 `go-redis/v9`、`segmentio/kafka-go`）
- `net/http` 标准库路由（Go 1.22 方法匹配语法）
- `log/slog` JSON 日志 + 自定义 `slog.Handler` 实现防篡改落盘

## 目录结构

```
.
├── main.go                         # 程序入口
├── config.yaml                     # 默认配置文件
├── internal/
│   ├── config/config.go            # 配置加载与默认值
│   ├── auth/auth.go                # Token 生成、缓存、轮换
│   ├── gateway/gateway.go          # HTTP 路由与 API 处理
│   ├── executor/
│   │   ├── executor.go             # 命令执行与安全过滤（通用逻辑）
│   │   ├── executor_sys_unix.go    # Unix 平台相关实现
│   │   └── executor_sys_windows.go # Windows 平台相关实现
│   ├── taskmanager/taskmanager.go  # 异步任务生命周期
│   ├── logentry/entry.go           # 结构化日志记录定义
│   ├── logger/                     # 防篡改日志 Handler、签名、校验
│   └── mq/                         # MQ 发送抽象（file/redis/kafka）
```

## 快速开始

### 1. 编译

```bash
# Linux（默认目标平台）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o test-agent .

# Windows
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o test-agent.exe .

# macOS
GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o test-agent .
```

项目使用 Go build tag 区分 Unix/Windows 平台实现，可直接在 Windows 上 `go build ./...` 编译。

### 2. 运行

```bash
./test-agent --config config.yaml
```

无需提前准备任何文件：启动时会自动生成防篡改日志所需的 Ed25519 签名密钥对（如已存在则复用，绝不会覆盖），并**在终端直接打印当日 API Token**，无需再打开 token 文件查看。

若希望手动管理密钥（例如由 CA 签发或在多台机器间分发同一公钥），可关闭自动生成：

```yaml
log:
  auto_generate_keys: false  # 或用环境变量 LOG_AUTO_GENERATE_KEYS=false
```

然后手动生成密钥对：

```bash
./test-agent --generate-keys --key-prefix agent-sign
```

会生成：

- `agent-sign.key`：私钥（权限 0600），用于对日志签名
- `agent-sign.pub`：公钥，用于消费者或运维人员校验日志完整性

### 3. 校验日志

```bash
./test-agent --config config.yaml --verify-log /var/log/agent/agent.log
```

> 注意：默认配置把日志写到 `/var/log/agent/agent.log`。在本地开发或 Windows 上运行时，建议将 `log.file` 改为 `./agent.log` 等当前用户可写的路径，或者使用 `LOG_FILE` 环境变量覆盖。

### 4. 把信息提供给 Claude Code

先把当前 `README.md` 或 `README_EN.md` 提供给 Claude Code 读取。
Agent 启动时会在终端打印当日 Token；如果终端输出已滚动，也可以直接读取 token 文件（如果 `auth.token_file` 在当前目录则为）：

```bash
cat .api_token
```

拿到当日 Token 后，把 IP 地址、端口（默认 28080）以及 Token 提供给 Claude Code，这样你就有了一台远程测试机。

每天开始任务时把最新 Token 提供给 Claude Code，安全无烦恼。

## 架构概览

```text
                    ┌─────────────────────────────────────┐
                    │           Controller/Client         │
                    │   (Claude Code / CI / 主控端)        │
                    └──────────────┬──────────────────────┘
                                   │ HTTP + Bearer Token
                                   ▼
                    ┌─────────────────────────────────────┐
                    │  Gateway (net/http)                 │
                    │  路由 / 鉴权 / 请求追踪 / panic 恢复   │
                    └──────────────┬──────────────────────┘
                                   │
           ┌───────────────────────┼───────────────────────┐
           │                       │                       │
           ▼                       ▼                       ▼
    ┌─────────────┐       ┌─────────────────┐      ┌──────────────┐
    │   Auth      │       │    Executor     │      │ TaskManager  │
    │ Token 生成/轮换│       │ 命令校验 / 执行   │      │ 异步任务生命周期 │
    └─────────────┘       └─────────────────┘      └──────────────┘
                                   │
                                   ▼
                    ┌─────────────────────────────────────┐
                    │  TamperProofLogger (slog.Handler)   │
                    │  seq + prev_hash + Ed25519 签名      │
                    └──────────────┬──────────────────────┘
                                   │
                    ┌──────────────┴──────────────┐
                    │                             │
                    ▼                             ▼
           本地 append-only JSONL           MQ (file/redis/kafka)
           /var/log/agent/agent.log          agent-logs
```

系统采用分层解耦架构：

1. **HTTP Gateway**：负责路由、鉴权、请求 ID 注入、panic 恢复与统一响应格式。
2. **Auth Manager**：负责 Token 的生成、内存缓存、定时轮换与原子文件写入，确保 Token 与系统用户权限绑定。
3. **Executor**：命令执行核心，包含白名单/黑名单校验、超时控制、进程组管理、输出截断。
4. **Task Manager**：异步任务生命周期管理（提交、查询、取消、过期清理）。
5. **Tamper-Proof Logger**：基于 `slog.Handler` 的防篡改日志层，负责结构化记录、签名、哈希链与 MQ 复制。

## API 接口

Base Path：`/api/v1`  
鉴权：`Authorization: Bearer <TOKEN>`（`/health` 除外）

### 健康检查

```bash
GET /api/v1/health
```

响应：
```json
{"code":0,"data":{"status":"alive"}}
```

### 同步执行命令

```bash
POST /api/v1/exec
```

请求体：
```json
{
  "command": "uname -a",
  "timeout": 30
}
```

响应：
```json
{
  "code": 0,
  "data": {
    "exit_code": 0,
    "stdout": "Linux pc-laptop ...",
    "stderr": "",
    "duration_ms": 12,
    "truncated": false
  }
}
```

### 提交异步任务

```bash
POST /api/v1/tasks
```

响应：
```json
{"code":0,"data":{"task_id":"task-20260709-a1b2c3"}}
```

### 查询/取消任务

```bash
GET    /api/v1/tasks/{task_id}
DELETE /api/v1/tasks/{task_id}
```

### 文件上传

```bash
POST /api/v1/upload
```

请求：`multipart/form-data`，字段名 `file`

```bash
curl -H "Authorization: Bearer <TOKEN>" \
     -F "file=@/path/to/test-program" \
     http://<host>:<port>/api/v1/upload
```

响应：
```json
{
  "code": 0,
  "data": {
    "original_name": "test-program",
    "saved_name": "upload_ac2e49c3_test-program",
    "saved_path": "/tmp/agent_workspace/uploads/upload_ac2e49c3_test-program",
    "size_bytes": 12345
  }
}
```

上传完成后可直接调用 `/exec` 执行：

```bash
curl -H "Authorization: Bearer <TOKEN>" \
     -H "Content-Type: application/json" \
     -d '{"command":"chmod +x /tmp/agent_workspace/uploads/upload_xxxx_test-program && /tmp/agent_workspace/uploads/upload_xxxx_test-program"}' \
     http://<host>:<port>/api/v1/exec
```

### 手动轮换 Token

```bash
POST /api/v1/auth/rotate
```

## 配置文件

```yaml
server:
  host: "0.0.0.0"
  port: 28080

auth:
  token_file: ".api_token"
  rotation_hour: 0
  token_length: 32

executor:
  default_timeout: 30
  max_timeout: 300
  max_output_size_mb: 10
  allowed_commands: []
  blocked_keywords:
    - "rm -rf /"
    - "mkfs"
    - "dd if=/dev/zero"
    - "shutdown"
    - ":\\(\\)\\{"
  work_dir: "/tmp/agent_workspace"
  upload_dir: ""                 # 空则使用 {work_dir}/uploads
  max_upload_size_mb: 64
  allowed_extensions: []         # 例: [".sh", ".py", ".bin"]

task_manager:
  max_running_tasks: 50
  retention_minutes: 60

log:
  enabled: true
  file: "/var/log/agent/agent.log"
  sign_key_file: "agent-sign.key"
  verify_pub_file: "agent-sign.pub"
  signature_algo: "ed25519"     # 可选 ed25519 | hmac-sha256
  console_output: true          # 同时输出到终端（stderr）
  auto_generate_keys: true      # 缺少签名密钥时自动生成；false 则要求手动 --generate-keys
  mq:
    type: "file"                # file | redis | kafka | noop
    topic: "agent-logs"
    file:
      dir: "./mq-spool"
    redis:
      addr: "localhost:6379"
      password: ""
      db: 0
      stream: "agent-logs"
    kafka:
      brokers:
        - "localhost:9092"
      topic: "agent-logs"
```

### 环境变量覆盖

| 环境变量 | 作用 |
|---|---|
| `SERVER_HOST` | 覆盖监听地址 |
| `SERVER_PORT` | 覆盖监听端口 |
| `AUTH_TOKEN_FILE` | 覆盖 Token 文件路径 |
| `AUTH_ROTATION_HOUR` | 覆盖 Token 轮换小时 |
| `EXECUTOR_WORK_DIR` | 覆盖工作目录 |
| `EXECUTOR_UPLOAD_DIR` | 覆盖上传目录 |
| `EXECUTOR_MAX_UPLOAD_SIZE_MB` | 覆盖最大上传大小 |
| `EXECUTOR_ALLOWED_EXTENSIONS` | 覆盖允许扩展名（逗号分隔） |
| `LOG_ENABLED` | 是否启用防篡改日志（true/false） |
| `LOG_FILE` | 覆盖日志文件路径 |
| `LOG_SIGN_KEY_FILE` | 覆盖签名私钥路径 |
| `LOG_CONSOLE_OUTPUT` | 是否同时输出到终端（true/false） |
| `LOG_MQ_TYPE` | 覆盖 MQ 类型（file/redis/kafka/noop） |
| `LOG_MQ_TOPIC` | 覆盖 MQ topic/stream 名 |
| `LOG_MQ_FILE_DIR` | 覆盖文件 MQ 目录 |
| `LOG_MQ_REDIS_ADDR` | 覆盖 Redis 地址 |
| `LOG_MQ_KAFKA_BROKERS` | 覆盖 Kafka broker 列表（逗号分隔） |

## 安全说明

1. **Token 不硬编码**：由 Agent 启动时自动生成并写入受 `0600` 权限保护的文件。
2. **每日轮换**：后台 Goroutine 在每日指定时间原子替换 Token。
3. **命令过滤**：命中黑名单或不在白名单中的命令会被拒绝。
4. **路径穿越防护**：上传接口会清洗文件名并校验保存路径是否在上传目录内。
5. **资源限制**：输出、上传文件、并发任务数均有上限。
6. **日志脱敏**：日志中不打印完整 Token，仅输出 SHA256 前 8 位前缀。
7. **日志完整性**：
   - 每条日志携带全局递增序列号 `seq`
   - 每条日志包含前一条日志的 SHA256 哈希 `prev_hash`，形成哈希链
   - 每条日志使用 Ed25519 私钥对内容签名，篡改任意字段都会导致校验失败
   - 日志异步复制到 MQ，实现生成端与存储端解耦
   - 提供 `./test-agent --verify-log` 命令离线校验本地日志

## 设计理念

详见 [DESIGN.md](DESIGN.md)，主要原则包括：

- **最小权限与解耦**：Agent 只负责生成日志，不持有最终存储权限；消费者独立部署、独立鉴权。
- **默认安全**：无硬编码凭据、Token 自动轮换、文件最小权限、命令黑白名单。
- **防御纵深**：从网络层（Token 鉴权）到进程层（PGID 清理）再到数据层（签名哈希链）的多层防护。
- **可验证的可观测性**：日志不仅是记录，更是可被密码学验证的证据链。
- **平台兼容**：同一套代码通过 build tag 支持 Linux、Windows、macOS。

## 部署建议

### 使用 systemd

```ini
[Unit]
Description=Test Agent
After=network.target

[Service]
Type=simple
ExecStart=/usr/local/bin/test-agent --config /etc/agent/config.yaml
Restart=always
User=root
WorkingDirectory=/home/pc/ssh-agent

[Install]
WantedBy=multi-user.target
```

### 手动后台运行

```bash
nohup ./test-agent --config config.yaml > /tmp/test-agent.log 2>&1 &
```

如果 `log.console_output` 已开启，日志也会出现在控制台上；使用 `nohup` 重定向时仍会保留文件日志，便于后续审计。

## 错误码

| HTTP Status | Code | 含义 |
|---|---|---|
| 200 | 0 | 成功 |
| 400 | 1001 | 参数错误 |
| 401 | 1002 | Token 缺失或不匹配 |
| 403 | 1003 | 命中黑名单/不在白名单/扩展名不允许 |
| 404 | 1004 | 任务ID不存在 |
| 408 | 1005 | 命令执行超时 |
| 429 | 1006 | 并发任务数超限 |
| 413 | 1011 | 文件过大 |
| 400 | 1012 | 文件名非法/路径穿越 |
| 500 | 2001 | 内部错误 |
| 500 | 2002 | 文件保存失败 |
| 409 | 2003 | 目标文件已存在 |

## 典型工作流

```bash
TOKEN=$(cat .api_token)
HOST=http://localhost:28080

# 1. 健康检查
curl $HOST/api/v1/health

# 2. 上传测试程序
curl -H "Authorization: Bearer $TOKEN" \
     -F "file=@./my-test" \
     $HOST/api/v1/upload

# 3. 执行测试程序
curl -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"command":"chmod +x /tmp/agent_workspace/uploads/upload_xxxx_my-test && /tmp/agent_workspace/uploads/upload_xxxx_my-test"}' \
     $HOST/api/v1/exec

# 4. 提交长时间运行的测试任务
curl -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"command":"./long-running-test","timeout":300}' \
     $HOST/api/v1/tasks
```

## License

MIT
