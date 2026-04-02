# Snap-CLI

State-aware checkpoints for AI coding agents (and humans).

Ever used an AI coding agent, let it run for a while, realized it messed up, and ran `git checkout HEAD~5` to revert the code? If you have, you probably noticed that while your code reverted, your database and your `.env` file didn't. 

Now your code expects one thing, but your database is in the future. This is called Agentic Drift, and it's super annoying to debug. 

Snap fixes this. It quietly hooks into Git so that whenever you change branches or checkout an old commit, it automatically restores your local databases and config files to match exactly how they were at that specific commit.

## How it works

The philosophy is simple: the Git commit hash should be the single source of truth for your entire system.

### Visual Architecture

```mermaid
graph TD
    subgraph "User Space"
        CLI["CLI Entrypoint (snap save / restore)"]
        GIT["Git Hooks (post-commit, checkout)"]
    end

    subgraph "Core Engine"
        ORCH["Orchestrator"]
        REG["Driver Registry"]
        MAN["Manifest Manager"]
    end

    subgraph "Storage Layer"
        CAS["CAS Object Store"]
        LEDGER["State Ledger (manifests)"]
        STAGING["Staging Area (tmp)"]
    end

    subgraph "State Drivers"
        SQLITE["SQLite Driver"]
        ENV["Dotenv Driver"]
    end

    CLI --> ORCH
    GIT --> ORCH
    ORCH --> REG
    ORCH --> MAN
    REG --> SQLITE
    REG --> ENV
    SQLITE --> STAGING
    ENV --> STAGING
    STAGING --> CAS
    MAN --> LEDGER
```

When you initialize Snap in a repository, it drops a small script into your native Git hooks:
- When you run `git commit`, Snap quietly streams your state (like a local SQLite DB or an env file) into a hidden content-addressable store (`.snap/objects`).
- When you run `git checkout`, Snap grabs the data associated with that checkout and puts it right back where it belongs.

I wrote this in pure Go with zero CGO dependencies (so it cross-compiles everywhere). The backing store does zero-cost deduplication, meaning if your database didn't actually change between commits, Snap doesn't duplicate the storage. It also streams everything through a tiny 32KB buffer, so it uses practically zero RAM even if you are tracking massive gigabyte-level databases.

## What it actually works with

Right now, Snap is built specifically for **local development environments**. The entire reason Snap exists is strictly for files that you cannot or should not track with Git natively (like secrets or giant binary databases). 

Because of this, you should NOT use Snap to track source code (`.ts`, `.py`, `.go`). Git already handles those perfectly! I built Snap to act as Git's sidekick, handling the heavy local files that it purposefully ignores:

* **.env files**: Fully supported. Snap captures them perfectly.
* **SQLite databases**: Fully supported for local dev. Right now Snap uses a blazing-fast file-copy mechanism under the hood. It takes a perfect snapshot of your `.sqlite` or `.db` file in milliseconds and streams it to the storage engine. (Just don't try to use it on a live production SQLite database that is taking 5,000 requests a second, it's not meant for that!).

**What it DOES NOT support (yet):**
* PostgreSQL, MySQL, Redis, or Mongo.
* Cloud-hosted databases. Snap physically cannot rewind your AWS RDS instance (and you probably wouldn't want it to!). 

Snap is purely for keeping your *local* environment perfectly synced with your *local* Git repository while you hack away or let an AI agent generate code. Building native driver hooks for local Docker Postgres/Redis instances is definitely on the roadmap!

## Installation

You just need Go installed on your machine. Run:

```bash
go install github.com/NishthaNabya/Snap-CLI/cmd/snap@latest
```

## Quick start

Go to any existing git repository and run:

```bash
snap init
```

This creates a `.snap/config.json` file. Edit it to tell Snap what files to track. For example, if you want local environments and a sqlite database tracked:

```json
{
  "entries": [
    {
      "driver": "dotenv",
      "source": ".env"
    },
    {
      "driver": "sqlite",
      "source": "database.sqlite"
    }
  ]
}
```

> [!WARNING]
> **CRITICAL SECURITY NOTE:** Because Snap stores copies of your `.env` files and databases locally, you **MUST** add the storage directories to your `.gitignore` file. If you don't, you will accidentally push your secret API keys to GitHub!
>
> Add this to your `.gitignore`:
> ```text
> .snap/objects/
> .snap/ledgers/
> .snap/snap.lock
> ```
> *(Note: It is perfectly safe to commit `.snap/config.json` so your team shares the same config!)*

That's literally it. You don't need to learn any new CLI commands. Just use Git like you always do.

```bash
# Make a change to your env
echo "SECRET=123" > .env
git add .env
git commit -m "added a secret"
# (Snap quietly backed up your .env in the background)

# Change it again
echo "SECRET=456" > .env
git commit -am "changed the secret"

# Watch Snap automatically revert your .env file on disk!
git checkout HEAD~1
```
