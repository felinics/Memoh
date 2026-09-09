<div align="right">
  <span>[<a href="./README.md">English</a>]<span>
  </span>[<a href="./README_CN.md">简体中文</a>]</span>
  </span>[<a href="./README_JA.md">日本語</a>]</span>
</div>  

<div align="center">
  <img src="./assets/logo.png" alt="Memoh" height="80">
  <h1>Memoh</h1>
  <p>Give every AI agent its own cloud computer. Open source.<br>
  Desktop, browser, network, and long-term memory — always on, even when your laptop is closed.</p>
  <div align="center">
    <img src="https://img.shields.io/github/package-json/v/felinics/Memoh" alt="Version" />
    <img src="https://img.shields.io/github/stars/felinics/Memoh?style=social" alt="Stars" />
    <img src="https://img.shields.io/github/forks/felinics/Memoh?style=social" alt="Forks" />
    <a href="https://deepwiki.com/felinics/Memoh">
      <img src="https://deepwiki.com/badge.svg" alt="DeepWiki" />
    </a>
    <a href="https://t.me/memohai">
      <img src="https://img.shields.io/badge/Telegram-Group-26A5E4?logo=telegram&logoColor=white" alt="Telegram" />
    </a>
  </div>
  <h3>
    <a href="https://memoh.ai/waitlist">Memoh Cloud</a> · <a href="#deploy-to-server">Deploy to Server</a> · <a href="https://docs.memoh.ai">Docs</a> · <a href="https://memoh.ai">Website</a> · <a href="https://x.com/memoh_ai">X</a>
  </h3>
  <img src="./assets/hero.png" alt="Memoh" width="1000">
</div>

## What is Memoh?

Memoh is an open-source multi-agent platform. Each agent gets its own cloud computer — a dedicated workspace with a filesystem, desktop, browser, network, and long-term memory. Your agents stay online 24/7, even when your laptop is closed.

Use your own API keys to run Memoh's built-in agent, or host your existing Claude Code and Codex agents inside Memoh workspaces.

Talk to them through Telegram, Discord, Lark, WeChat, Web UI, and more. They remember context across sessions and platforms, drive a browser, call MCP tools, and run scheduled tasks. Run one for yourself, assign one to each team member, or spin up a fleet.

## Get Started

### Memoh Cloud

> [!TIP]
> Memoh Cloud is coming soon — zero setup, always-on agents in the cloud. Join the waitlist at [memoh.ai/waitlist](https://memoh.ai/waitlist).

### Deploy to Server

Self-host the full stack on your own infrastructure.

```bash
curl -fsSL https://memoh.sh | sh
```

<details>
<summary><strong>More deployment options</strong></summary>

Manual deployment:

```bash
git clone --depth 1 --recurse-submodules --shallow-submodules https://github.com/felinics/Memoh.git
cd Memoh
cp conf/app.docker.toml config.toml
# Edit config.toml
export MEMOH_INTERNAL_RPC_SHARED_SECRET="$(openssl rand -hex 32)"
docker compose up -d
```

The Compose stack runs Server and Channel as separate services. Keep the internal RPC secret private and use the same value whenever the stack is recreated.

Running without Docker (or upgrading an existing bare-metal install)? Leave `internal_rpc.shared_secret` empty in `config.toml`: the server then embeds the channel runtime and keeps running as a single all-in-one process — external channels, email, and webhook endpoints included — with no `memoh-channel` process required. Setting the secret opts into the split two-process deployment.

Existing setup checkouts can keep using `git pull`: the post-merge hook initializes the new
submodule, and setup enables recursive updates for future pulls. If that hook was never installed,
run `mise run submodule-init` once after upgrading.
GitHub's automatic “Source code” archives omit submodule contents. For a buildable release archive,
use the attached `Memoh-<version>-source.zip` or `.tar.gz` asset instead.

> **Use CN mirror for slow image pulls:**
> ```bash
> curl -fsSL https://memoh.sh | USE_CN_MIRROR=true sh
> ```
>
> Do not run the whole installer with `sudo`. The installer will use `sudo docker`
> internally if Docker requires it.

See [DEPLOYMENT.md](DEPLOYMENT.md) for custom configuration and production setup.

</details>

## Why Memoh?

- **Every agent gets its own computer**: An isolated workspace with its own filesystem, network, desktop, and browser.
- **Multi-user, multi-bot**: Run one for yourself, deploy one for each family member, run a fleet on a single machine.
- **Lightweight**: Self-host the server on your own infrastructure, or connect through Memoh Cloud.

## Features

- **Multi-bot & multi-user**: Multiple bots that chat privately, in groups, or with each other. Cross-platform identity binding.
- **Isolated workspaces**: Each bot has a dedicated filesystem, network, tools, and desktop.
- **Built-in memory**: Long-term memory across sessions and platforms, out of the box. Also supports [Mem0](https://mem0.ai), OpenViking.
- **10+ channels**: Telegram, Discord, Lark, WeChat, QQ, Email, and more.
- **MCP**: Connect external tool servers. Each bot manages its own connections.
- **Agent Hosting**: Host external agents inside Memoh workspaces via ACP. Currently supports Codex and Claude Code, configured per bot.
- **Browser Use**: Drive a browser inside the workspace.
- **Computer Use**: Operate the workspace desktop for GUI workflows.
- **Skills & Supermarket**: Modular skills, install curated templates from Supermarket, delegate to sub-agents.
- **Automation**: Scheduled tasks for recurring workflows.

## Sub-projects

- [**Twilight AI**](https://github.com/felinics/twilight) — A lightweight, idiomatic AI SDK for Go, inspired by [Vercel AI SDK](https://sdk.vercel.ai/). Provider-agnostic (OpenAI, Anthropic, Google), with first-class streaming, tool calling, MCP, and embeddings.
- [**Connect It**](https://github.com/felinics/connect-it) — A self-hosted connector gateway that securely stores SaaS credentials and gives agents access to integrations through a single MCP endpoint.
- [**UI**](https://github.com/felinics/ui) — A Vue 3 design system for AI agent management interfaces, including a component library, design tokens, and skills that teach agents how to use them.

## Project Status

![License](https://img.shields.io/github/license/felinics/Memoh) ![Last Commit](https://img.shields.io/github/last-commit/felinics/Memoh) ![Commit Activity](https://img.shields.io/github/commit-activity/m/felinics/Memoh) ![Issues](https://img.shields.io/github/issues/felinics/Memoh) ![Pull Requests](https://img.shields.io/github/issues-pr/felinics/Memoh)

## Star History

[![Star History Chart](https://star-history.dera.page/svg?repos=felinics/Memoh&type=date&legend=top-left)](https://star-history.dera.page/#felinics/Memoh&type=date&legend=top-left)

## Contributors

<a href="https://github.com/felinics/Memoh/graphs/contributors">
  <img src="https://contrib.rocks/image?repo=felinics/Memoh" />
</a>

## Community

- 🌐 [**Website**](https://memoh.ai)
- 📚 [**Documentation**](https://docs.memoh.ai)
- 💬 [**Telegram Group**](https://t.me/memohai)
- 🛒 [**Supermarket**](https://github.com/felinics/supermarket)
- 🤝 [**Cooperation**](mailto:business@memoh.net) — business@memoh.net

---

**LICENSE**: AGPLv3

Made with ❤️ by MemohAI Team,

Copyright (C) 2026 MemohAI (memoh.ai). All rights reserved.
