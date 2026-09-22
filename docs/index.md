---
title: svkexe
description: Self-hosted platform for persistent Linux VMs with an integrated PicoClaw coding agent
---

svkexe runs persistent Linux VMs (Incus containers) for several users behind one gateway. Each VM comes with a web shell, SSH access through a gateway, an LLM proxy and the PicoClaw coding agent with the Shelley web UI.

- [Deployment guide](/docs/DEPLOY): prerequisites, bare-metal installer, Docker Compose, updates.
- [API reference](/docs/API): authentication, VM lifecycle, keys, LLM proxy endpoints.
- [SSH interface](/docs/SSH): the management shell, direct VM access, and the JSON command catalogue for LLM agents.
- [Named VM access](/docs/ACCESS): guest accounts, SSH keys and revocation.
- [PicoClaw agent](/docs/PICOCLAW): how the agent is built into the VM image and configured per VM.

The project README on GitHub covers installation and configuration in full: [github.com/skrashevich/svkexe](https://github.com/skrashevich/svkexe). The standalone agent source package is described in [agent/README.md](https://github.com/skrashevich/svkexe/blob/main/agent/README.md).
