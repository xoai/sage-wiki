# Self-Hosting sage-wiki: Your Second Brain, Anywhere

sage-wiki can run on a server — a VPS, a NAS, a Raspberry Pi, a home lab machine — giving you access to your personal knowledge base from anywhere. Drop sources on your laptop, they sync to the server, sage-wiki compiles them, and you browse your wiki from any device with a browser.

This guide covers Docker setup, file syncing, and common deployment patterns.

## Quick Start with Docker

```bash
# Pull from GitHub Container Registry
docker pull ghcr.io/xoai/sage-wiki:latest

# Or from Docker Hub
docker pull xoai/sage-wiki:latest

# Run with your wiki directory mounted
docker run -d \
  --name sage-wiki \
  -p 3333:3333 \
  -v /path/to/your/wiki:/wiki \
  -e GEMINI_API_KEY=your-key-here \
  ghcr.io/xoai/sage-wiki
```

Open `http://your-server:3333` in a browser. That's it.

The default command starts the web UI server. Your wiki directory is mounted at `/wiki` inside the container.

### Available tags

| Tag | When | Use case |
|-----|------|----------|
| `:latest` | Every push to `main` | Living on the edge |
| `:v1.0.0` | GitHub Releases | Stable, predictable |
| `:sha-abc1234` | Every push | Pinning a specific build |

Multi-arch images: `linux/amd64` and `linux/arm64` (Raspberry Pi, ARM servers).

### Building from source

If you prefer to build locally:

```bash
git clone https://github.com/xoai/sage-wiki.git
cd sage-wiki
docker build -t sage-wiki .
```

## Docker Compose

For a more complete setup with auto-restart and persistent data:

```yaml
# docker-compose.yml
services:
  sage-wiki:
    image: ghcr.io/xoai/sage-wiki:latest
    # Or build from source:
    # build: .
    ports:
      - "3333:3333"
    volumes:
      - ./my-wiki:/wiki
    environment:
      - GEMINI_API_KEY=${GEMINI_API_KEY}
      # Or use other providers:
      # - ANTHROPIC_API_KEY=${ANTHROPIC_API_KEY}
      # - OPENAI_API_KEY=${OPENAI_API_KEY}
    restart: unless-stopped
```

```bash
docker compose up -d
```

## Container Commands

The default entrypoint is `sage-wiki`. Override the command for different modes:

```bash
# Web UI (default)
docker run -v ./wiki:/wiki -p 3333:3333 -e GEMINI_API_KEY=... ghcr.io/xoai/sage-wiki

# Compile once and exit
docker run -v ./wiki:/wiki -e GEMINI_API_KEY=... ghcr.io/xoai/sage-wiki compile

# Watch mode — recompile on file changes
docker run -v ./wiki:/wiki -e GEMINI_API_KEY=... ghcr.io/xoai/sage-wiki compile --watch

# MCP server (SSE transport for remote agents)
docker run -v ./wiki:/wiki -p 3333:3333 -e GEMINI_API_KEY=... ghcr.io/xoai/sage-wiki serve --transport sse --port 3333

# Query your wiki
docker run -v ./wiki:/wiki -e GEMINI_API_KEY=... ghcr.io/xoai/sage-wiki query "What is self-attention?"

# Interactive shell (override entrypoint)
docker run -it --entrypoint sh -v ./wiki:/wiki ghcr.io/xoai/sage-wiki
```

## Syncing with Syncthing

[Syncthing](https://syncthing.net/) keeps your wiki directory in sync across devices. This is the recommended pattern for a self-hosted setup:

```
┌──────────────┐     Syncthing      ┌──────────────────────┐
│   Laptop     │ ◄────────────────► │   Server             │
│              │                    │                      │
│  raw/        │  ← you write here  │  raw/     (synced)   │
│  prompts/    │                    │  prompts/ (synced)   │
│  wiki/       │  ← read compiled   │  wiki/    (compiled) │
│  config.yaml │                    │  config.yaml         │
└──────────────┘                    │                      │
                                    │  sage-wiki compile   │
                                    │    --watch           │
                                    └──────────────────────┘
```

### Setup

1. Install Syncthing on both your laptop and server
2. Share your wiki project directory between them
3. On the server, run sage-wiki in watch mode:

```bash
docker run -d \
  --name sage-wiki-compiler \
  -v /path/to/synced/wiki:/wiki \
  -e GEMINI_API_KEY=your-key \
  sage-wiki compile --watch
```

4. Optionally run the web UI alongside:

```bash
docker run -d \
  --name sage-wiki-web \
  -p 3333:3333 \
  -v /path/to/synced/wiki:/wiki \
  -e GEMINI_API_KEY=your-key \
  sage-wiki serve --ui --bind 0.0.0.0
```

Or combine both in a compose file:

```yaml
x-common: &common
  build: .
  volumes:
    - ./wiki:/wiki
  environment:
    - GEMINI_API_KEY=${GEMINI_API_KEY}
  restart: unless-stopped

services:
  compiler:
    <<: *common
    command: ["compile", "--watch"]

  web:
    <<: *common
    # default command: serve --ui --bind 0.0.0.0 --port 3333
    ports:
      - "3333:3333"
```

### Workflow

1. Drop a PDF, markdown file, or notes into `raw/` on your laptop
2. Syncthing pushes it to the server
3. sage-wiki detects the change and compiles (summarize → extract → write)
4. The compiled wiki syncs back to your laptop
5. Open in Obsidian locally, or browse via the web UI from any device

### Conflict avoidance

Syncthing can produce conflicts if both sides modify the same file. To avoid this:

- **Only write to `raw/` and `prompts/` from your laptop** — these are your source files
- **Never edit `wiki/` manually** — it's compiled output, regenerated by the server
- **`config.yaml` is shared** — edit it from one device only
- **`.sage/wiki.db` should not be synced** — add it to Syncthing's ignore list:

```
// .stignore (Syncthing ignore file)
.sage/wiki.db
.sage/wiki.db-wal
.sage/wiki.db-shm
.sage/compile-state.json
.sage/lintlog
```

## LLM Providers

sage-wiki works with any LLM provider. Set the provider and API key in your `config.yaml` (inside the mounted wiki directory) and pass the key as an environment variable.

### Gemini (default)

```yaml
# config.yaml
api:
  provider: gemini
  api_key: ${GEMINI_API_KEY}
```

```bash
docker run -e GEMINI_API_KEY=your-key ...
```

### Anthropic

```yaml
api:
  provider: anthropic
  api_key: ${ANTHROPIC_API_KEY}
```

```bash
docker run -e ANTHROPIC_API_KEY=your-key ...
```

### OpenAI

```yaml
api:
  provider: openai
  api_key: ${OPENAI_API_KEY}
```

```bash
docker run -e OPENAI_API_KEY=your-key ...
```

### OpenAI-Compatible (OpenRouter, Together, Groq, Azure, etc.)

Any provider with an OpenAI-compatible API works. Set `provider: openai-compatible` and point `base_url` to the endpoint:

```yaml
# OpenRouter
api:
  provider: openai-compatible
  base_url: https://openrouter.ai/api/v1
  api_key: ${OPENROUTER_API_KEY}
models:
  summarize: google/gemini-2.5-flash-preview
  extract: google/gemini-2.5-flash-preview
  write: anthropic/claude-sonnet-4
  query: anthropic/claude-sonnet-4
```

```bash
docker run -e OPENROUTER_API_KEY=your-key ...
```

Other examples:

```yaml
# Together AI
api:
  provider: openai-compatible
  base_url: https://api.together.xyz/v1
  api_key: ${TOGETHER_API_KEY}

# Groq
api:
  provider: openai-compatible
  base_url: https://api.groq.com/openai/v1
  api_key: ${GROQ_API_KEY}

# Azure OpenAI
api:
  provider: openai-compatible
  base_url: https://your-resource.openai.azure.com/openai/deployments/your-deployment/
  api_key: ${AZURE_OPENAI_API_KEY}
```

### Local LLMs (Ollama, vLLM, LM Studio)

Run the LLM on the same server — no API key needed, no data leaves your network:

```yaml
api:
  provider: ollama
  base_url: http://host.docker.internal:11434  # Ollama on Docker host
```

Or with any OpenAI-compatible local server:

```yaml
api:
  provider: openai-compatible
  base_url: http://host.docker.internal:8000/v1  # vLLM, LM Studio, etc.
  api_key: not-needed
```

With Docker Compose, you can run Ollama alongside sage-wiki:

```yaml
services:
  ollama:
    image: ollama/ollama
    volumes:
      - ollama-data:/root/.ollama

  sage-wiki:
    build: .
    ports:
      - "3333:3333"
    volumes:
      - ./wiki:/wiki
    environment:
      - OLLAMA_HOST=http://ollama:11434
    depends_on:
      - ollama

volumes:
  ollama-data:
```

This gives you a fully self-contained, private knowledge base — no external API calls at all.

## Reverse Proxy with HTTPS

For internet access, put sage-wiki behind a reverse proxy. Example with Caddy:

```
# Caddyfile
wiki.yourdomain.com {
    reverse_proxy localhost:3333
}
```

Or Nginx:

```nginx
server {
    listen 443 ssl;
    server_name wiki.yourdomain.com;

    ssl_certificate /etc/letsencrypt/live/wiki.yourdomain.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/wiki.yourdomain.com/privkey.pem;

    location / {
        proxy_pass http://localhost:3333;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
    }
}
```

**Important:** sage-wiki has no built-in authentication. If you expose it to the internet, add auth at the reverse proxy layer (e.g., Caddy's `basicauth`, Authelia, Cloudflare Access).

## Deploying on a VPS

Minimal steps for a fresh Ubuntu/Debian VPS:

```bash
# Install Docker
curl -fsSL https://get.docker.com | sh

# Initialize a wiki project
mkdir -p ~/my-wiki/raw
cd ~/my-wiki
docker run --rm -v .:/wiki ghcr.io/xoai/sage-wiki init --model gemini-2.5-flash

# Set your API key
export GEMINI_API_KEY=your-key-here

# Run
docker run -d \
  --name sage-wiki \
  -p 3333:3333 \
  -v ~/my-wiki:/wiki \
  -e GEMINI_API_KEY \
  --restart unless-stopped \
  ghcr.io/xoai/sage-wiki
```

## Raspberry Pi / ARM

sage-wiki is pure Go (zero CGO), so it cross-compiles to ARM:

```bash
# Build for ARM64 (Raspberry Pi 4/5)
docker build --platform linux/arm64 -t sage-wiki .

# Or build the binary directly
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags webui -o sage-wiki ./cmd/sage-wiki
```

## Resource Usage

sage-wiki is lightweight:

- **Memory:** ~30-50 MB at rest, spikes during compile (depends on source count)
- **CPU:** Negligible except during compile (LLM calls are the bottleneck, not local compute)
- **Disk:** The binary is ~32 MB. Wiki data depends on your sources.
- **Network:** Outbound HTTPS to your LLM provider. No inbound needed unless you expose the web UI.

A $5/month VPS or a Raspberry Pi is more than enough.

## Troubleshooting

**Container exits immediately:** Check logs with `docker logs sage-wiki`. Common causes: missing API key, invalid config.yaml, or no sources to compile.

**Syncthing conflicts:** Check `.stignore` excludes database files. See the conflict avoidance section above.

**Timezone:** Set the `TZ` environment variable or configure `compiler.timezone` in config.yaml:

```bash
docker run -e TZ=Asia/Shanghai ...
```

**Permission issues:** The container runs as UID 1000. If it can't write to the mounted volume, match the user:

```bash
# Option 1: Run as your host user
docker run --user $(id -u):$(id -g) -v ./wiki:/wiki sage-wiki

# Option 2: Make the directory owned by UID 1000
chown -R 1000:1000 /path/to/wiki
```
