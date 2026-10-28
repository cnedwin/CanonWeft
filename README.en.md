# CanonWeft · 砚台

[中文](README.md) · [English](README.en.md) · [GitHub](https://github.com/cnedwin/CanonWeft)

CanonWeft is a local-first studio for stories, characters, and shared worlds. It organizes novel writing around shared canon, versions and branches, retrieval, durable tasks, and creative memory. Novels are the current entry point; comic, game, and other cross-media production modules remain on the roadmap.

## Project status

The project is in development. The current installation and release scripts target **Windows x86-64 on a local machine**. Linux, Docker, and public-domain deployment are not yet supported by this installation path. See the [implementation status](docs/IMPLEMENTATION_STATUS.md) for delivered workflows and remaining validation.

- Eight genre presets, a one-sentence story start, and an editable plan for confirmation.
- World and story planning → book/volume outline → chapter plan → draft candidates → bookkeeping and independent review → explicit adoption.
- Editable character cards, relationships, versioned shared canon, and character-change impact review.
- A staged writing desk, on-demand context, selection/chapter revision, and batches of up to ten chapter candidates.
- MySQL persistence, local BGE-M3 hybrid retrieval, layered memory, and source tracking.
- Versioned methods and capabilities, external Skill candidates, task pause/resume, budgets and reconciliation, backups, and isolated restoration.

Presets and test doubles do not establish real-model writing quality. Long-form consistency, literary quality, editorial outcomes, and the effects of specified image models still require separate validation.

## Local installation

Run from the repository root:

```powershell
.\Install-Platform.cmd
.\Start-Platform.cmd
```

Open **http://127.0.0.1:18765/**. The first run creates a studio and a personal account; there is no shared account or default password. Installation downloads and verifies pinned dependencies. It does not purchase cloud resources or call text/image models.

See the [local installation guide](docs/LOCAL_INSTALL.md) for prerequisites, downloads, stopping services, existing-data protection, and supported platforms. Most detailed project documentation is currently in Chinese.

## Models and costs

Existing protocol adapters cover OpenAI, Anthropic, DeepSeek, Qwen, and Gemini. The studio owner configures credentials locally and binds a provider configuration to each required purpose. Saving a key does not generate content.

Model calls require valid configuration, a price version, an explicitly enabled budget, and server-side network access. Provider network access is disabled by default. Saving configuration or a budget does not purchase credit. An unknown request outcome retains its budget reservation and is not automatically retried.

Built-in planning and first-chapter trial drafts do not require completing the entire qualification suite first. Certified capabilities and formal publication through independent review still require the applicable purpose and version qualifications. See [model setup](docs/MODELS_AND_NOVEL_ENTRY.md) and [capability qualification](docs/CAPABILITY_QUALIFICATION.md).

## Technical baseline

| Component | Current baseline |
|---|---|
| Frontend | Vue 3.5.44, TypeScript, Vite |
| Application service | Go 1.27.3, Echo v5, database/sql |
| Database | MySQL 9.7.4 Community |
| Durable tasks | Self-hosted Temporal |
| Retrieval and ingestion | Python 3.12.15, ONNX CPU embeddings, OCR |
| Data and files | MySQL, immutable local objects, protected revocation records |

Pinned versions, sources, hashes, and supported platforms are recorded in `deploy/locks/`, `backend/go.mod`, and `web/pnpm-lock.yaml`. Python supports retrieval and document processing; business services use Go. Older prototypes are retained for historical compatibility reference.

## Documentation and contribution

- [Documentation index](docs/README.md), [product design](docs/FINAL_PLAN.md), [requirements](docs/REQUIREMENTS_TRACE.md), and [roadmap](docs/DELIVERY_ROADMAP.md).
- [Architecture](docs/ARCHITECTURE.md), [unified memory](docs/UNIFIED_MEMORY_ARCHITECTURE.md), and [coding standards](docs/CODING_STANDARDS.md).
- [Contributing](CONTRIBUTING.md), [security](SECURITY.md), and [third-party notices](THIRD_PARTY_NOTICES.md).

## License

The main project code is licensed under the [Apache License 2.0](LICENSE). Third-party fonts, PDF libraries, dependencies, models, and Skill snapshots retain their own licenses and notices; see [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) and [NOTICE](NOTICE). The project license does not relicense those materials or require authors' novel text to be open source.
