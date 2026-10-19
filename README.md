<div align="center">

# 🛡️ IronClaveBot

### The Sovereign, Sandboxed AI Teammate for Developers
*An open-source initiative by [DevelopersGlobal](https://github.com/DevelopersGlobal)*

[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](./LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go&logoColor=white)](https://golang.org)
[![Docker](https://img.shields.io/badge/Docker-Ready-2496ED?logo=docker&logoColor=white)](./Dockerfile)
[![Sandboxed](https://img.shields.io/badge/Security-Zero--Escape_Sandbox-emerald)](./internal/sandbox)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](./CONTRIBUTING.md)

<p align="center">
  <b>Persistent AI Teammates</b> • <b>Zero Host Escapes</b> • <b>Single Static Binary (<10MB)</b> • <b>Embedded Web UI</b> • <b>Zero-Config Local Memory</b>
</p>

</div>

---

## ⚡ What is IronClaveBot?

**IronClaveBot** is an open-source, self-hosted alternative to heavy computer-use bots and proprietary agent workspaces (Grok Bot, Rakazo, OpenClaw).

While existing agent platforms require complicated Node.js runtime versions, heavy PostgreSQL installations, Redis queues, and multi-gigabyte Docker Compose sprawls, **IronClaveBot compiles into a single static Go binary** with an embedded web dashboard and persistent memory.

### 🛡️ Why "IronClave"?
Giving an AI agent direct terminal and filesystem access is dangerous. IronClaveBot isolates all tool executions inside a **hardened, restricted sandbox enclave**:
- **Directory Boundary Validation:** Agent operations cannot traverse (`../`) outside the isolated workspace.
- **Destructive Command Blocker:** Automatic circuit-breakers for harmful system calls (`rm -rf /`, raw disk writes, fork bombs).
- **Zero-Cloud Telemetry:** Use local models via Ollama or bring your own API keys. No third-party data tracking.

---

## 🎥 Demo

https://github.com/user-attachments/assets/e5873013-a8a5-4539-8de5-45c65511d56f

*Live run: IronClaveBot with a small local model (qwen2.5:0.5b via Ollama) inside a sandboxed container - the agent lists the workspace, reads `main.go`, and explains the Go project.*

---

## 🚀 10-Second Quickstart

### Option 1: Run with Go (Zero Dependencies)

```bash
git clone https://github.com/DevelopersGlobal/ironclavebot.git
cd ironclavebot

# Build and start the embedded server
make run
```
Open **[http://localhost:8080](http://localhost:8080)** in your browser!

### Option 2: Run with Docker

```bash
docker run -p 8080:8080 -v $(pwd)/workspace:/ironclave/workspace ghcr.io/developersglobal/ironclavebot:latest
```

### Option 3: Terminal CLI Mode (No Browser Required)

```bash
./bin/ironclavebot -chat "Inspect workspace files and summarize what you see"
```

---

## 📊 How IronClaveBot Compares

| Feature | IronClaveBot 🛡️ | Rakazo / Grok Bot | AutoGPT / OpenClaw |
| :--- | :--- | :--- | :--- |
| **Setup Time** | **< 30 seconds** | 10–20 minutes | 15+ minutes |
| **Dependencies** | **Single Binary (<10MB)** | Node 22/24, Postgres, Prisma, Redis | Python, venv, Poetry, Docker |
| **Memory Footprint** | **~25 MB RAM** | ~350 MB+ RAM | ~500 MB+ RAM |
| **Web Dashboard** | **Embedded in Binary** | Separate React/Vite server | Separate Next.js app |
| **Execution Sandbox** | **Hardened Enclave Sandbox** | Full Docker / VM required | Host process / Unrestricted |
| **License** | **Apache 2.0 (Open Source)**| Apache 2.0 | Mixed / Business |

---

## 🏗️ Architecture

```
┌─────────────────────────────────────────────────────────────┐
│             Web UI Dashboard (Embedded HTML/JS)             │
└──────────────────────────────┬──────────────────────────────┘
                               │ HTTP / WebSocket REST API
┌──────────────────────────────▼──────────────────────────────┐
│           IronClaveBot Core Engine (Golang Binary)          │
│  ┌─────────────────────────┐   ┌─────────────────────────┐  │
│  │ Persistent Store        │   │ Autonomous ReAct Loop   │  │
│  │ (Embedded SQLite / WAL) │   │ (Multi-Turn Reasoning)  │  │
│  └─────────────────────────┘   └────────────┬────────────┘  │
└─────────────────────────────────────────────┼───────────────┘
                                              │
                      ┌───────────────────────┴───────────────────────┐
                      ▼                                               ▼
         ┌─────────────────────────┐                     ┌─────────────────────────┐
         │   AI Provider Gateway   │                     │  Isolated Sandboxed FS  │
         │ • Ollama (Local/Offline)│                     │ • Path Traversal Guard  │
         │ • OpenAI / DeepSeek     │                     │ • Command Blacklist     │
         │ • Anthropic / Groq      │                     │ • Ephemeral Workspace   │
         └─────────────────────────┘                     └─────────────────────────┘
```

---

## ⚙️ Configuration (`.env`)

Copy `.env.example` to `.env` to configure your preferred LLM. `.env` is read at startup;
variables already set in your shell take precedence over it.

```bash
# LLM Provider: openai | ollama | mock
LLM_PROVIDER=openai
OPENAI_API_KEY=your_key_here
LLM_MODEL=gpt-4o

# For 100% Offline Local Inference:
# LLM_PROVIDER=ollama
# OLLAMA_BASE_URL=http://localhost:11434
# LLM_MODEL=llama3.2
```

---

## 🔐 Security notes

- The dashboard listens on `127.0.0.1` by default. Binding another address (`HOST` / `-host`)
  requires `API_TOKEN`, and every `/api` request must then send `Authorization: Bearer <token>`.
- `SECURITY_MODE=interactive` (default) and `strict` refuse command execution and file writes
  because there is no approval mechanism yet. `permissive` allows them; use it only inside a
  container or VM.
- `run_command` is **not** an isolation boundary. Commands run as the daemon's user with the
  host filesystem and network available (only the working directory, a minimal environment, a
  30 s timeout, an output cap and a coarse blacklist apply). Run IronClaveBot in a container or
  VM when giving it a model you do not fully trust.

---

## 🤝 Community & Contributing

Built with ❤️ by the **[DevelopersGlobal](https://github.com/DevelopersGlobal)** community.

We welcome all contributions:
- Adding Model Context Protocol (MCP) servers
- Voice streaming (WebRTC / Opus)
- WASM-based plugin extensions
- UI improvements

Read [CONTRIBUTING.md](./CONTRIBUTING.md) to get started!
