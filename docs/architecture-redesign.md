# Architecture: AI Code Agent System

## Context

We need a system that:

- Watches for Jira tickets requesting code changes
- Uses AI agents (Claude Code, Gemini CLI, etc.) to implement those changes
- Validates the changes using the target project's own toolchain
- Creates GitHub PRs with the results
- Handles PR review feedback in subsequent cycles

The current prototype proves the concept works but has fundamental problems: the AI can't validate because it lacks the toolchain, validation fixes fail because context is lost between AI invocations, and the system can't leverage AI-native workflows defined in target repos.

This document describes the architecture we'd build from scratch.

## Design Principles

**Bot manages jobs and plumbing. AI acts autonomously.** The bot decides *what* needs doing (which ticket, which repo). The AI decides *how* to do it (what code to write, how to validate, how to fix). The bot never constructs step-by-step instructions for the AI.

**The container is the sandbox.** The AI runs with full permissions (`--dangerously-skip-permissions` / `--y`) inside an ephemeral dev container. The container provides safety isolation -- the AI can't push to git, access Jira, or affect the host. The container is destroyed after each job.

**The environment is the interface.** Instead of complex prompt templates, the bot communicates with the AI through the environment: the repo (with its CLAUDE.md, Makefile, CI config), a task description file, and the dev container's toolchain. The AI reads the environment and acts accordingly.

**Teams own their environments.** Teams provide a dev container image with their toolchain AND their chosen AI CLI installed. The bot doesn't need to know about C++, Java, Go, or Python. It doesn't inject tools into containers.

**Jira and GitHub are the state store.** No database, no message queue. Ticket status = job state. PR comments = feedback queue. If the bot crashes, it recovers by querying Jira for stuck tickets.

## Architecture

### Components

#### 1. Event Loop (Scanners)

Polls external systems for work. Produces events, not actions.

- **WorkItemScanner**: Queries Jira for tickets in "todo" status matching configured projects/ticket types. Emits `NewTicket` events.
- **FeedbackScanner**: Queries Jira for tickets in "in review" status, then checks GitHub for new PR comments since last processed timestamp. Applies bot-loop-prevention filters (ignored users, known bots, thread depth). Emits `NewFeedback` events.

Both scanners are stateless -- they query Jira/GitHub each cycle. No in-memory tracking of "what we've seen before" is needed because the Job Manager handles deduplication.

#### 2. Job Manager

The central coordinator. Receives events from scanners, creates jobs, enforces constraints.

Responsibilities:

- **Deduplication**: Won't create a job for a ticket that already has one running (keyed by ticket ID)
- **Concurrency control**: Semaphore limiting max parallel jobs (protects VM resources when 20 users are active)
- **Cost tracking**: Accumulates daily spend, pauses job creation when budget exceeded
- **Retry policy**: Failed jobs can be retried with backoff (configurable per job type)
- **Crash recovery**: On startup, queries Jira for tickets stuck in "In Progress" with no PR. Re-queues them.
- **Workspace tracking**: Maintains a mapping of ticket ID to workspace directory path. Used by the Job Executor to find or create workspaces. Cleaned up when tickets leave active states or via TTL (see [Workspace Lifecycle](#workspace-lifecycle)).

Not a database-backed queue. Job state lives in memory during execution. Durable state lives in Jira (ticket status) and GitHub (PR existence). This is sufficient because:

- If a job succeeds, Jira and GitHub reflect the result
- If a job fails cleanly, the ticket status is reverted (retried next scan)
- If the bot crashes mid-job, the crash recovery query catches it on restart

#### 3. Job Executor

Executes a single job. This is where the work happens, but critically, the executor handles plumbing -- the AI handles thinking.

A job executor does these things and ONLY these things:

**a. Prepare the workspace**

- Check whether a workspace already exists for this ticket (via Job Manager's workspace tracking).
- **If no workspace exists** (first job for this ticket):
  - Clone the repo (using GitHub App installation token)
  - Create a branch (`{bot-username}/{ticket-key}`)
  - If fork mode: ensure fork exists, clone the fork instead
  - Register the workspace path with the Job Manager
- **If a workspace exists** (subsequent jobs, e.g. PR feedback):
  - Sync the workspace with the remote branch: `git fetch origin && git reset --hard origin/<branch>`
  - This picks up any commits made since the last job -- including human developer commits pushed to the fork, and the bot's own prior API-created commits
  - **Untracked files survive `git reset --hard`**, so AI-generated artifacts (caches, indexes, workflow state) from previous sessions are preserved

**b. Write the task file**

Creates `/workspace/.ai-bot/task.md` with the goal:

- For new tickets: ticket summary, description, acceptance criteria
- For PR feedback: the PR diff, review comments grouped by file, what's new vs. already addressed

This is a simple markdown file. Not a prompt template with Go variables. The AI reads it like a developer would read a task description.

**c. Start the dev container**

- Resolve container config (see [Dev Container Strategy](#dev-container-strategy))
- Launch the container with the repo mounted at `/workspace`
- Inject environment variables: `AI_PROVIDER`, API keys, `PROJECT_DIR=/workspace`
- Apply resource limits (memory, CPU, timeout)

**d. Launch the AI agent**

- Run the AI CLI inside the container as the container's main process
- The command is simple: `claude --dangerously-skip-permissions -p "Read /workspace/.ai-bot/task.md and complete the task described there."` (or Gemini equivalent)
- The AI takes it from there -- reads the task, reads the repo, writes code, runs validation, fixes issues, iterates
- The executor waits for the process to exit (or kills it on timeout)

**e. Collect results**

- After the AI exits, diff the repo: `git diff` and `git status` in the workspace
- If no changes: report failure (or retry per policy)
- Extract any output/summary the AI left (e.g., in a designated output file)

**f. Commit and create PR**

- Commit all changes via GitHub API (verified commits, co-author attribution)
- **Post-commit sync**: After the API commit succeeds, reconcile the local workspace with the remote:
  ```
  git fetch origin
  git reset --hard origin/<branch>
  ```
  This is necessary because commits are created via the GitHub API (for verified signatures), not via local `git commit` + `git push`. Without this sync, the local git state diverges from what GitHub has -- the workspace has the right file content but the wrong git history. The sync fixes this while preserving untracked artifacts.
- Generate PR description (short AI invocation, or parse from AI session output)
- Create PR (draft if the AI reported validation failures, normal otherwise)
- For PR feedback jobs: push new commits to existing PR branch, reply to comments

**g. Update issue tracker**

- Set PR URL on the ticket (custom field or structured comment)
- Transition ticket status (to "In Review" for new tickets)
- Post error comments if the job failed (unless disabled)

**h. Cleanup**

- Stop and remove the container
- **Do not delete the workspace directory** -- it is retained for potential reuse by subsequent jobs on the same ticket (e.g., PR feedback). Workspace cleanup is managed by the [Workspace Lifecycle](#workspace-lifecycle) process.

#### 4. IssueTracker (interface)

```go
type IssueTracker interface {
    SearchWorkItems(criteria SearchCriteria) ([]WorkItem, error)
    GetWorkItem(key string) (*WorkItem, error)
    TransitionStatus(key, status string) error
    AddComment(key, body string) error
    GetFieldValue(key, field string) (string, error)
    SetFieldValue(key, field, value string) error
}
```

- `WorkItem` is a generic type: key, summary, description, type, components, assignee, security level
- `SearchCriteria` abstracts JQL (Jira) or search queries (future issue trackers)
- Jira is the only implementation initially, but nothing above this interface is Jira-specific
- Clean boundary enables adding GitHub Issues, GitLab Issues, etc. later

#### 5. GitService (interface)

```go
type GitService interface {
    // Workspace operations
    Clone(repoURL, dir string) error
    CreateBranch(dir, name string) error
    GetDiff(dir string) (string, error)
    HasChanges(dir string) (bool, error)
    SyncWithRemote(dir, branch string) error  // git fetch && git reset --hard origin/<branch>

    // API operations (verified commits, no CLI git push)
    CommitChanges(owner, repo, branch, dir, message string, coAuthor *Author) error
    CreatePR(params PRParams) (*PR, error)
    UpdatePR(owner, repo string, number int, params PRUpdateParams) error

    // PR interaction
    GetPRDetails(owner, repo string, number int) (*PRDetails, error)
    GetPRComments(owner, repo string,
                  number int, since time.Time) ([]PRComment, error)
    ReplyToComment(owner, repo string, prNumber int,
                   commentID int64, body string) error
    PostPRComment(owner, repo string, prNumber int, body string) error

    // Fork management
    EnsureFork(upstream, forkOwner string) error

    // Auth
    GetInstallationToken(owner, repo string) (string, error)
}
```

- GitHub App only -- no PAT mode
- `CommitChanges` reads the working directory diff and creates verified commits via the GitHub Git Data API (blob -> tree -> commit -> update ref)
- `SyncWithRemote` reconciles the local workspace after an API commit (see [Post-commit sync](#f-commit-and-create-pr))
- Supports both fork-based PRs (open source model) and direct-branch PRs, configured per project
  - Fork mode: GitHub App installed on user's fork, commits there, PRs to upstream
  - Direct mode: GitHub App creates branch on target repo, PRs from branch

#### 6. ContainerManager

```go
type ContainerManager interface {
    ResolveConfig(repoDir string) (*ContainerConfig, error)
    Start(ctx context.Context, config *ContainerConfig, workspace string,
          env map[string]string) (*Container, error)
    Exec(ctx context.Context, ctr *Container,
         cmd []string) (output string, exitCode int, err error)
    Stop(ctx context.Context, ctr *Container) error
    CleanupOrphans(prefix string) error
}
```

Behind this, a `ContainerRuntime` interface abstracts Podman vs Docker (auto-detected, Podman preferred for rootless security).

### Dev Container Strategy

#### How teams configure their environment

Teams provide a container image that has:

- Their project's toolchain (compiler, linter, test framework, etc.)
- Their chosen AI CLI (`claude-code`, `gemini-cli`, etc.)

The bot discovers the container config in priority order:

1. `.ai-bot/container.json` -- bot-specific container config
2. `.devcontainer/devcontainer.json` -- standard devcontainer spec
3. Bot's `global default_container_image` -- admin-configured fallback
4. Built-in minimal fallback (includes common AI CLIs)

#### What the bot reads from devcontainer config

A practical subset: `image`, `build.dockerfile`, `build.context`, `postCreateCommand`, `containerEnv`. Not the full devcontainer spec -- just enough to launch a working container. Unsupported fields logged and ignored.

#### What happens with no container config

The bot uses a minimal fallback image that has AI CLIs pre-installed but no project-specific toolchain. The AI can still generate code but can't validate. This is the lowest-friction path -- teams get value immediately, then add a container for better results.

#### The contract

The container receives:

- Repo mounted at `/workspace` (or configurable mount point)
- Environment variables: `PROJECT_DIR`, `AI_PROVIDER`, API keys for the AI service
- Resource limits enforced by the container runtime

The container must provide:

- A working shell
- The selected AI CLI on `$PATH`
- Network access to the AI API endpoint

The container does **not** receive:

- GitHub tokens (the bot commits via API after the AI finishes)
- Jira credentials
- Any access to the host system

#### Toolchain secrets

**Open question**: Teams may need secrets for their toolchain (private package registries, license servers, git submodule access, internal APIs). The bot injects AI API keys, but toolchain-specific secrets need a separate mechanism. Options under consideration include bot-managed secret injection, host-mounted secrets directories, and container-side vault fetching. This will be addressed when implementing the ContainerManager.

#### Artifact persistence across sessions

AI workflows may generate filesystem artifacts that need to survive across container invocations for the same ticket (e.g., documentation indexes, analysis caches, workflow state). These artifacts persist because:

- They reside as untracked files in the workspace directory
- The workspace is scoped to the ticket lifetime, not the job lifetime
- `git reset --hard` (used to sync with remote) only affects tracked files

**Convention**: AI-generated artifacts that should persist across sessions must be placed in untracked locations. Recommended locations:

- `.ai-bot/cache/` -- general-purpose artifact storage (add to `.gitignore`)
- Standard build output directories already in `.gitignore` (e.g., `build/`, `dist/`, `node_modules/`)
- Any path covered by the project's `.gitignore`

Teams should ensure their `.gitignore` covers artifact directories. The bot will not run `git clean` on reused workspaces.

## How the AI Operates (the key architectural difference)

### The bot gives a goal, not instructions

The bot does NOT construct prompts like:

> Here is ticket PROJ-123. Implement the fix. Then run `make build`. If it fails, fix the errors. Then run `make lint`. If it fails...

Instead, the bot writes a task file and tells the AI to read it:

**For new tickets:**

```markdown
<!-- /workspace/.ai-bot/task.md -- written by bot, read by AI -->
# Task: PROJ-123

## Summary
Fix null pointer exception in UserService.getProfile()

## Description
When a user has no profile photo set, calling getProfile() throws a
NullPointerException at UserService.java:142. The photo URL field should
default to a placeholder image.

## Acceptance Criteria
- getProfile() returns a valid response when photo is null
- Unit tests cover the null photo case
- All existing tests still pass

## Instructions
Implement this task. Validate your changes compile and pass tests using
whatever build tools this project provides. Fix any issues you find.
Do not push to git -- the system handles that.
```

**For PR feedback:**

```markdown
<!-- /workspace/.ai-bot/task.md -- written by bot, read by AI -->
# Task: Address PR Review Feedback

## PR Context
PR #42: Fix null pointer in UserService.getProfile()
Branch: ai-bot/PROJ-123

## Review Comments

### File: src/main/java/com/example/UserService.java
**@reviewer1** (line 145):
> This should use Optional<String> instead of a null check.
> Our codebase convention is to use Optional for nullable returns.

### General
**@reviewer2**:
> Please add an integration test, not just a unit test.

## Instructions
Address each review comment. Validate your changes compile and pass tests.
Do not push to git -- the system handles that.
```

The AI -- running inside the dev container with the full toolchain -- reads this file, reads the repo (including CLAUDE.md, Makefile, CI config, whatever exists), and does whatever it takes to complete the task. If CLAUDE.md defines a `/bug-fix` skill, the AI can use it. If there's a Makefile with `make test`, the AI will run it. The bot doesn't need to know any of this.

### Why this is better than prompt engineering

- **The AI is already an agent.** Claude Code and Gemini CLI know how to explore codebases, run commands, fix errors, and iterate. Giving them step-by-step instructions actually makes them worse -- it overrides their own judgment.
- **It's naturally team-agnostic.** A Java team's AI session will `mvn test`. A Go team's will `make lint`. A Python team's will `pytest`. The bot doesn't need to know which -- the AI reads the project and figures it out.
- **AI-native workflows just work.** If a team has CLAUDE.md with skills, the AI uses them. No bot-side integration needed. The bot doesn't even know they exist.
- **Context is complete.** The AI has the full repo, the full toolchain, and a clear goal. It makes its own decisions about what to validate and how to fix failures -- with full context from its own code generation.

### `.ai-bot/config.yaml` -- team hints (optional)

Teams can provide hints that both the bot and AI consult:

```yaml
# .ai-bot/config.yaml -- optional, lives in the repo

# Hints for the AI (AI reads this file directly)
validation_commands:
  - make build
  - make lint
  - make test

# Settings for the bot (bot reads these for PR creation)
pr:
  draft: false
  title_prefix: "[AI]"
  labels: ["ai-generated"]

# AI provider preferences
ai:
  claude:
    allowed_tools: "Bash Edit Read Write"
  gemini:
    model: "gemini-2.5-pro"
```

This is a hints file, not an orchestration script. The `validation_commands` section tells the AI "these are our validation commands" -- but the AI decides when and how to run them. The `pr` section tells the bot how to create the PR. The `ai` section configures provider-specific settings.

If this file doesn't exist, the AI figures things out from the repo itself (Makefile, CI config, etc.). The file reduces guesswork and wasted tokens, but isn't required.

## Workspace Lifecycle

Workspaces are scoped to **tickets**, not jobs. A single workspace directory persists across all jobs for the same ticket, enabling AI-generated artifacts to survive between sessions.

### Lifecycle

1. **Created** when the first job for a ticket clones the repo
2. **Reused** by subsequent jobs (PR feedback, retries) -- synced with remote via `git fetch && git reset --hard`
3. **Destroyed** when the workspace is no longer needed

### Cleanup triggers

- **Ticket status transition**: When a ticket moves out of active states (e.g., "Done", "Closed", "Won't Fix"), the workspace can be cleaned up. The bot detects this during scan cycles.
- **TTL expiry**: Workspaces older than a configurable maximum age (e.g., 7 days since last job) are cleaned up regardless of ticket status. Prevents unbounded disk growth from abandoned tickets.
- **Startup cleanup**: On bot startup, scan for orphaned workspaces (workspace exists but ticket is in a terminal state or no longer assigned to the bot). Clean up alongside orphaned containers.

### Disk management

- Workspaces are stored under a configurable base directory (e.g., `/var/lib/ai-bot/workspaces/`)
- Directory naming convention: `<ticket-key>/` (e.g., `PROJ-123/`)
- The Job Manager tracks active workspace paths in memory; the filesystem is the source of truth for cleanup

### What survives between jobs

| Content | Survives? | Why |
|---------|-----------|-----|
| Committed source files | Yes | `git reset --hard` updates them to match remote |
| Human developer commits | Yes | `git fetch` pulls them before reset |
| Bot's prior API commits | Yes | `git fetch` pulls them before reset |
| Untracked artifacts (`.ai-bot/cache/`, build output) | Yes | `git reset --hard` does not touch untracked files |
| Uncommitted modifications | No | `git reset --hard` discards them (this is correct -- they were already committed via API) |
| Container filesystem (outside mount) | No | Container is destroyed after each job |

## Bot-Level Configuration

```yaml
logging:
  level: info
  format: json

issue_tracker:
  type: jira
  jira:
    base_url: https://your-domain.atlassian.net
    username: bot-user
    api_token: ${JIRA_API_TOKEN}
    poll_interval_seconds: 300

github:
  app_id: 123456
  private_key_path: /secrets/github-app-key.pem
  bot_username: ai-code-bot
  target_branch: main
  # Bot loop prevention
  max_thread_depth: 5
  known_bot_usernames: [github-actions, dependabot, coderabbitai]
  ignored_usernames: [packit-as-a-service]

ai:
  default_provider: claude
  session_timeout_seconds: 1800

container:
  runtime: auto
  default_image: our-org/ai-dev-base:latest
  resource_limits:
    memory: 8g
    cpus: 4

workspaces:
  base_dir: /var/lib/ai-bot/workspaces
  ttl_days: 7                    # clean up workspaces older than this

projects:
  - project_keys: [PROJ1]
    pr_strategy: fork            # or "direct"
    ai_provider: claude          # override default
    status_transitions:
      Bug: { todo: "Open", in_progress: "In Progress", in_review: "Code Review" }
      Story: { todo: "To Do", in_progress: "In Progress", in_review: "In Review" }
    component_to_repo:
      backend: https://github.com/org/backend.git
      frontend: https://github.com/org/frontend.git
    pr_url_field: "Git Pull Request"

guardrails:
  max_concurrent_jobs: 10
  max_retries: 3
  max_daily_cost_usd: 100.00
  max_container_runtime_minutes: 60
```

Note what's **not** here compared to the prototype:

- No `repo_validations` -- validation is the AI's job, configured in-repo
- No `allowed_tools` / `disallowed_tools` at bot level -- per-repo in `.ai-bot/config.yaml`
- No prompt templates -- the bot writes a task file, not a templated prompt
- No AI CLI paths -- the CLI is in the container, not on the bot

## What the Bot Owns vs. What the AI Owns

| Concern | Owner | Why |
|---------|-------|-----|
| Polling for work | Bot | Infrastructure plumbing |
| Job lifecycle (create, track, retry, cancel) | Bot | Coordination logic |
| Repo cloning, branching | Bot | Needs GitHub App tokens |
| Dev container lifecycle | Bot | Infrastructure plumbing |
| Workspace lifecycle (create, reuse, cleanup) | Bot | Infrastructure plumbing |
| Writing the task file | Bot | Translates events into goals |
| Understanding the problem | AI | Requires reasoning |
| Writing code | AI | Requires domain knowledge |
| Running build/lint/test | AI | Requires toolchain + context |
| Fixing failures | AI | Requires context from generation |
| Using repo workflows/skills | AI | AI-native, bot doesn't understand |
| Deciding when it's "done" | AI | Judgment call |
| Committing (via API) | Bot | Verified commits, needs App auth |
| Post-commit workspace sync | Bot | Reconciles local git state after API commit |
| Creating/updating PRs | Bot | Needs App auth |
| Replying to PR comments | Bot | Needs App auth, loop prevention |
| Status transitions | Bot | Needs Jira auth |
| Crash recovery | Bot | Needs to detect stuck jobs |

## Security Model

The container is the security boundary:

| Resource | AI has access? | How |
|----------|---------------|-----|
| Source code | Yes | Mounted at `/workspace` |
| Team's toolchain | Yes | Installed in container |
| AI API (Anthropic/Google) | Yes | API key in env var |
| GitHub API | No | No token in container |
| Jira API | No | No credentials in container |
| Host filesystem | No | Container isolation |
| Network (general) | Configurable | Container network policy |
| Other containers | No | Container isolation |
| Persistent workspace artifacts | Yes | Untracked files in mounted workspace |

The AI runs with `--dangerously-skip-permissions` / `--y` because the container IS the permission boundary. The AI can do anything inside the container (run commands, modify files, install packages) without risk to the host or external systems.

After the AI finishes:

1. The bot diffs the repo to see what changed
2. The bot commits via GitHub API (not the AI)
3. The bot syncs the workspace with the API-created commit
4. The bot creates the PR (not the AI)
5. The container is destroyed (workspace is retained)

## Error Handling & Edge Cases

| Scenario | Behavior |
|----------|----------|
| AI session times out | Container killed. Ticket status reverted. Error comment posted (unless disabled). Retried next scan cycle. Workspace retained for retry. |
| AI produces no changes | Retried up to `max_retries`. After exhaustion, error comment, status reverted. Workspace retained. |
| AI reports validation failures | Draft PR created with failure summary. Human reviewer can see what the AI tried. |
| Dev container fails to build | Fallback to minimal container. If fallback also fails, error comment, skip. |
| Bot crashes mid-job | On restart, crash recovery queries Jira for "In Progress" tickets with no PR. Orphaned containers cleaned up. Orphaned workspaces detected and re-associated or cleaned up. Jobs re-queued. |
| Cost budget exceeded | Job creation paused. Existing jobs finish. Resumes next day (or when budget reset). |
| Concurrent job limit reached | New jobs queued (not dropped). Processed when a slot opens. |
| AI tries to push to git | Fails -- no git credentials in container. This is by design. |
| Workspace disk full | Job fails with clear error. TTL-based cleanup frees space. Configurable disk usage alerts. |

### Crash Recovery

On startup, the bot:

1. Cleans up orphaned containers (prefix-based filter)
2. Scans the workspace base directory for existing workspaces
3. Queries Jira for tickets assigned to the bot in "In Progress" status
4. For each:
   - If no PR exists: the job was interrupted. Re-queue it (workspace may be reusable).
   - If PR exists but ticket is still "In Progress": the status transition was interrupted. Complete it.
5. Cleans up workspaces for tickets in terminal states (Done, Closed, etc.)

This is possible because Jira and GitHub are the durable state store. No separate database needed. Workspaces on disk are discoverable by naming convention (`<ticket-key>/`).

## Technology Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Language | Go | Good concurrency model for managing parallel jobs. Team knows it. Compiles to a single binary. |
| Container runtime | Podman (preferred), Docker (supported) | Podman is rootless by default. Auto-detect at startup. |
| GitHub integration | `google/go-github` + `ghinstallation` | Already proven in prototype. Mature libraries. |
| Configuration | Viper | YAML + env var support. Already proven. |
| Logging | zap | Structured logging. Already proven. |
| State persistence | None (Jira + GitHub + filesystem) | Simplest correct approach for the scale. Workspaces on disk are the only local state. |
| Job queue | In-process (channel + goroutines) | Sufficient for per-team deployment. No external dependencies. |

## Implementation Phases

### Phase 1: Core Abstractions

- Define `IssueTracker`, `GitService`, `ContainerManager` interfaces
- Define `WorkItem`, `Job`, `Container` types
- Define `AIProvider` interface (build command, parse output)
- Implement Jira `IssueTracker`
- Implement GitHub `GitService` (App-only, drop PAT paths)
  - Include `SyncWithRemote` for post-commit workspace reconciliation

### Phase 2: Container Management

- Implement `ContainerManager` with devcontainer.json parsing
- Implement `ContainerRuntime` for Podman/Docker
- Dev container resolution (repo config -> global default -> fallback)
- Container lifecycle: start, exec, stop, cleanup orphans
- Resource limit enforcement

### Phase 3: Job System

- Implement `JobManager` (creation, deduplication, concurrency, lifecycle tracking, workspace tracking)
- Implement `JobExecutor` (the clone/reuse -> container -> AI -> collect -> commit -> sync -> PR pipeline)
- Task file generation (ticket -> markdown, feedback -> markdown)
- Workspace lifecycle management (TTL, terminal-state cleanup)
- Crash recovery on startup (containers, workspaces, stuck tickets)
- Implement scanners (work item scanner, feedback scanner)

### Phase 4: AI Provider Integration

- Claude provider (build command, parse stream-json output)
- Gemini provider (build command, parse output)
- Provider selection per project
- Session timeout enforcement

### Phase 5: PR Feedback Loop

- Feedback scanner with bot-loop-prevention filters
- PR comment grouping and timestamp tracking
- Feedback task file generation
- Comment reply posting
- Workspace reuse with artifact preservation

### Phase 6: Guardrails & Polish

- Cost tracking and daily budget
- Per-repo `.ai-bot/config.yaml` parsing (hints file)
- Fork-based PR workflow
- End-to-end testing

## Verification

- **Unit tests**: Each interface implementation tested with mocks
- **Container integration test**: Verify container lifecycle (resolve -> start -> exec -> stop -> cleanup) with a real container runtime
- **End-to-end test**: Process a test ticket against a real repo with a dev container, verify PR creation
- **Crash recovery test**: Kill the bot mid-job, restart, verify recovery (including workspace re-association)
- **Concurrency test**: Submit 20 jobs simultaneously, verify semaphore enforcement
- **Feedback test**: Post PR comments, verify the bot processes and replies correctly, verify workspace reuse and artifact preservation
- **Loop prevention test**: Verify bot doesn't reply to itself or other bots beyond thread depth
- **Workspace lifecycle test**: Verify workspaces are cleaned up on ticket closure and TTL expiry
- Run `make lint` after all code changes
