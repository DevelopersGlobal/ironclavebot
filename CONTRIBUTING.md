# Contributing to IronClaveBot 🛡️

We welcome contributors from all over the world! **IronClaveBot** is an open-source initiative by **[DevelopersGlobal](https://github.com/DevelopersGlobal)** to create a zero-trust, ultra-lightweight, persistent AI teammate for developers.

---

## 🚀 Quick Setup (Under 2 Minutes)

You only need **Go 1.22+** installed:

```bash
git clone https://github.com/DevelopersGlobal/ironclavebot.git
cd ironclavebot

# Build the single static binary
make build

# Start in development mode (starts embedded web dashboard at http://localhost:8080)
make run
```

---

## 🛠️ How to Add a New Tool

Tools live in `internal/tools/`. To add a new capability (e.g. Git PR triage, Postgres query runner, Web scraper):

1. Implement the `Tool` interface:
   ```go
   type Tool interface {
       Name() string
       Description() string
       Parameters() map[string]interface{}
       Execute(ctx context.Context, argsJSON string) (string, error)
   }
   ```
2. Register your tool in `cmd/ironclavebot/main.go`:
   ```go
   registry.Register(tools.NewMyCustomTool(box))
   ```
3. Submit a Pull Request!

---

## 📜 Pull Request Guidelines

1. **Keep it Sandboxed:** Any tool performing disk or terminal actions must execute through the `sandbox.Sandbox` boundary.
2. **Zero Bloat:** We avoid heavy CGO bindings to keep the binary cross-compilable everywhere in seconds.
3. **Tests:** Ensure `go test ./...` passes before submitting.
