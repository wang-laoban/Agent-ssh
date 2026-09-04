# test-agent

A lightweight remote command execution agent that runs on test machines. It provides an HTTP RESTful API as an alternative to interactive SSH, allowing a controller to dispatch commands, run scripts, upload files, and collect execution results remotely.

## Features

- **Remote Command Execution**: synchronous (`POST /exec`) and asynchronous (`POST /tasks`) execution modes.
- **File Upload**: distribute test programs or scripts via `POST /api/v1/upload`.
- **Dynamic Token Authentication**: tokens are automatically rotated daily and written to a permission-protected file, avoiding hard-coded secrets.
- **Security Filtering**: command blacklist/whitelist, path traversal protection, file size limits, and extension whitelist.
- **Resource Limits**: command timeout, output truncation (default 10MB), and maximum upload size (default 64MB).
- **Process Management**: on Unix, process groups (PGID) are used so the entire process tree is killed on timeout/cancellation; on Windows the main process is terminated directly.
- **Graceful Shutdown**: listens for SIGTERM/SIGINT, waits for running tasks, then shuts down the HTTP server.
- **Tamper-Proof Logging**: structured logs are persisted locally with Ed25519 signatures, SHA256 hash chains, and global sequence numbers. Logs are replicated asynchronously to MQ and can be verified offline.
- **Key Pair Management**: built-in command to generate signing key pairs and verify persisted logs.

## Use Cases

### Remote Test Machine for AI Coding Assistants

Deploy `test-agent` on a test machine to serve as a remote execution environment for tools like Claude Code, replacing manual SSH login:

- **No SSH exposure required**: the controller dispatches commands via HTTP API + dynamic token; no SSH private keys or passwords need to be shared.
- **Ready to use**: start the agent on the target machine and remotely execute commands, upload scripts, and collect output.
- **Complete execution records**: all command results, timing, and output are persisted as JSON logs for auditing, tracing, and troubleshooting.
- **Safe and controllable**: blacklist/whitelist, timeout, output limits, path traversal protection, and other mechanisms prevent accidental or malicious operations from affecting production.

### CI/CD and Regression Testing

- Call `/api/v1/exec` or `/api/v1/tasks` in CI/CD pipelines to run test scripts remotely.
- Use `/api/v1/upload` to distribute binaries or test packages, then collect results after execution.
- Asynchronous tasks are suitable for long-running regression tests; the controller can poll or use callbacks to fetch results.

## Tech Stack

- Go 1.22+ (standard library + `gopkg.in/yaml.v3`; optional MQ dependencies `go-redis/v9`, `segmentio/kafka-go`)
- `net/http` standard library routing (Go 1.22 method matching syntax)
- `log/slog` JSON logging + custom `slog.Handler` for tamper-proof persistence

## Project Structure

```
.
├── main.go                         # Application entry point
├── config.yaml                     # Default configuration file
├── internal/
│   ├── config/config.go            # Configuration loading and defaults
│   ├── auth/auth.go                # Token generation, caching, rotation
│   ├── gateway/gateway.go          # HTTP routing and API handlers
│   ├── executor/
│   │   ├── executor.go             # Command execution and security filtering
│   │   ├── executor_sys_unix.go    # Unix-specific implementation
│   │   └── executor_sys_windows.go # Windows-specific implementation
│   ├── taskmanager/taskmanager.go  # Asynchronous task lifecycle
│   ├── logentry/entry.go           # Structured log record definition
│   ├── logger/                     # Tamper-proof log handler, signing, verification
│   └── mq/                         # MQ sender abstraction (file/redis/kafka)
```

## Quick Start

### 1. Build

```bash
# Linux (default target)
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o test-agent .

# Windows
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o test-agent.exe .

# macOS
GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o test-agent .
```

The project uses Go build tags to distinguish Unix/Windows implementations; you can build directly on Windows with `go build ./...`.

### 2. Run

```bash
./test-agent --config config.yaml
```

No preparation is needed: on startup the agent automatically generates the Ed25519 signing key pair required by tamper-proof logging (existing keys are reused and never overwritten), and **prints the current API Token directly to the terminal**, so you no longer need to open the token file.

If you prefer to manage keys manually (e.g. issued by a CA, or share one public key across machines), disable auto-generation:

```yaml
log:
  auto_generate_keys: false  # or set LOG_AUTO_GENERATE_KEYS=false
```

Then generate the key pair manually:

```bash
./test-agent --generate-keys --key-prefix agent-sign
```

This generates:

- `agent-sign.key`: private key (permission 0600), used to sign logs.
- `agent-sign.pub`: public key, used by consumers or operators to verify log integrity.

### 3. Verify Logs

```bash
./test-agent --config config.yaml --verify-log /var/log/agent/agent.log
```

> Note: the default configuration writes logs to `/var/log/agent/agent.log`. For local development or Windows environments, change `log.file` to a writable path such as `./agent.log`, or override it with the `LOG_FILE` environment variable.

### 4. Provide Token to Claude Code

First share this README with Claude Code.
The agent prints today's token to the terminal on startup; if the output has scrolled away, read the token file directly (if `auth.token_file` is in the current directory):

```bash
cat .api_token
```

Copy today's token, along with the IP address and port (default 28080), to Claude Code. You now have a remote test machine.

Provide the latest token to Claude Code at the start of each day's work for secure, hassle-free access.

## API Reference

Base Path: `/api/v1`  
Authentication: `Authorization: Bearer <TOKEN>` (except `/health`)

### Health Check

```bash
GET /api/v1/health
```

Response:
```json
{"code":0,"data":{"status":"alive"}}
```

### Execute Command Synchronously

```bash
POST /api/v1/exec
```

Request body:
```json
{
  "command": "uname -a",
  "timeout": 30
}
```

Response:
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

### Submit Asynchronous Task

```bash
POST /api/v1/tasks
```

Response:
```json
{"code":0,"data":{"task_id":"task-20260709-a1b2c3"}}
```

### Query / Cancel Task

```bash
GET    /api/v1/tasks/{task_id}
DELETE /api/v1/tasks/{task_id}
```

### File Upload

```bash
POST /api/v1/upload
```

Request: `multipart/form-data`, field name `file`

```bash
curl -H "Authorization: Bearer <TOKEN>" \
     -F "file=@/path/to/test-program" \
     http://<host>:<port>/api/v1/upload
```

Response:
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

After upload, execute directly via `/exec`:

```bash
curl -H "Authorization: Bearer <TOKEN>" \
     -H "Content-Type: application/json" \
     -d '{"command":"chmod +x /tmp/agent_workspace/uploads/upload_xxxx_test-program && /tmp/agent_workspace/uploads/upload_xxxx_test-program"}' \
     http://<host>:<port>/api/v1/exec
```

### Manual Token Rotation

```bash
POST /api/v1/auth/rotate
```

## Configuration

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
  upload_dir: ""                 # empty means {work_dir}/uploads
  max_upload_size_mb: 64
  allowed_extensions: []         # e.g. [".sh", ".py", ".bin"]

task_manager:
  max_running_tasks: 50
  retention_minutes: 60

log:
  enabled: true
  file: "/var/log/agent/agent.log"
  sign_key_file: "agent-sign.key"
  verify_pub_file: "agent-sign.pub"
  signature_algo: "ed25519"     # ed25519 | hmac-sha256
  console_output: true          # also echo logs to stderr
  auto_generate_keys: true      # auto-create signing keys if missing; false requires --generate-keys
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

### Environment Variable Overrides

| Variable | Purpose |
|---|---|
| `SERVER_HOST` | Override listen address |
| `SERVER_PORT` | Override listen port |
| `AUTH_TOKEN_FILE` | Override token file path |
| `AUTH_ROTATION_HOUR` | Override token rotation hour |
| `EXECUTOR_WORK_DIR` | Override working directory |
| `EXECUTOR_UPLOAD_DIR` | Override upload directory |
| `EXECUTOR_MAX_UPLOAD_SIZE_MB` | Override maximum upload size |
| `EXECUTOR_ALLOWED_EXTENSIONS` | Override allowed extensions (comma-separated) |
| `LOG_ENABLED` | Enable/disable tamper-proof logging (true/false) |
| `LOG_FILE` | Override log file path |
| `LOG_SIGN_KEY_FILE` | Override signing key path |
| `LOG_CONSOLE_OUTPUT` | Echo logs to stderr (true/false) |
| `LOG_MQ_TYPE` | Override MQ type (file/redis/kafka/noop) |
| `LOG_MQ_TOPIC` | Override MQ topic/stream name |
| `LOG_MQ_FILE_DIR` | Override file MQ directory |
| `LOG_MQ_REDIS_ADDR` | Override Redis address |
| `LOG_MQ_KAFKA_BROKERS` | Override Kafka broker list (comma-separated) |

## Security Notes

1. **No hard-coded token**: the token is automatically generated at startup and written to a file protected by `0600` permissions.
2. **Daily rotation**: a background goroutine rotates the token atomically at the configured local time.
3. **Command filtering**: commands matching the blacklist or not in the whitelist are rejected.
4. **Path traversal protection**: the upload handler sanitizes filenames and verifies the save path is within the upload directory.
5. **Resource limits**: output, upload size, and concurrent task count are all bounded.
6. **Log desensitization**: full tokens are never logged; only the first 8 hex characters of the SHA256 hash are printed.
7. **Log integrity**:
   - Every log entry carries a globally increasing `seq`.
   - Every log entry contains the SHA256 hash of the previous entry (`prev_hash`), forming a hash chain.
   - Every log entry is signed with the Ed25519 private key; tampering with any field causes verification to fail.
   - Logs are replicated asynchronously to MQ, decoupling producers from storage.
   - Use `./test-agent --verify-log` to verify local logs offline.

## Deployment

### Using systemd

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

### Manual Background Run

```bash
nohup ./test-agent --config config.yaml > /tmp/test-agent.log 2>&1 &
```

If `log.console_output` is enabled, logs are also visible on the console. When using `nohup` redirection, the tamper-proof file log is still preserved for later auditing.

## Error Codes

| HTTP Status | Code | Meaning |
|---|---|---|
| 200 | 0 | Success |
| 400 | 1001 | Bad request |
| 401 | 1002 | Missing or mismatched token |
| 403 | 1003 | Blacklisted / not in whitelist / extension not allowed |
| 404 | 1004 | Task not found |
| 408 | 1005 | Command execution timeout |
| 429 | 1006 | Too many concurrent tasks |
| 413 | 1011 | File too large |
| 400 | 1012 | Invalid filename / path traversal |
| 500 | 2001 | Internal error |
| 500 | 2002 | File save failed |
| 409 | 2003 | Target file already exists |

## Typical Workflow

```bash
TOKEN=$(cat .api_token)
HOST=http://localhost:28080

# 1. Health check
curl $HOST/api/v1/health

# 2. Upload test program
curl -H "Authorization: Bearer $TOKEN" \
     -F "file=@./my-test" \
     $HOST/api/v1/upload

# 3. Execute test program
curl -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"command":"chmod +x /tmp/agent_workspace/uploads/upload_xxxx_my-test && /tmp/agent_workspace/uploads/upload_xxxx_my-test"}' \
     $HOST/api/v1/exec

# 4. Submit long-running test task
curl -H "Authorization: Bearer $TOKEN" \
     -H "Content-Type: application/json" \
     -d '{"command":"./long-running-test","timeout":300}' \
     $HOST/api/v1/tasks
```

## Design Philosophy

See [DESIGN.md](DESIGN.md) for the complete architecture, security model, and evolution of the tamper-proof logging system.

## License

MIT
