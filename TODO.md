# TODO

## Approach

This project evolves incrementally within the existing repository (strangler
fig pattern). New abstractions and implementations are introduced alongside
existing code. The existing pipeline continues to work throughout the
transition. Deprecated code is removed at cutover.

Design document: `docs/architecture-redesign.md`

---

## Epic 1: AI Code Agent System Redesign

Rearchitect the system so the AI runs autonomously inside ephemeral dev
containers with full toolchain access. The bot manages jobs and plumbing
(cloning, committing, PR creation, status transitions). The AI manages
thinking (code generation, validation, fixing failures). Workspaces persist
across jobs for the same ticket, enabling AI-generated artifacts to survive
between sessions.

### Task 1: Core domain types and IssueTracker interface

Introduce the generic domain model that decouples the system from Jira.
Implement a Jira adapter that wraps the existing `JiraService` behind the
new interface, proving the abstraction works without breaking the existing
pipeline.

**Scope**

- Define `WorkItem` type (key, summary, description, type, components,
  assignee, security level) -- the generic representation of a work item
  independent of any issue tracker
- Define `SearchCriteria` type abstracting JQL and future query languages
- Define `IssueTracker` interface: `SearchWorkItems`, `GetWorkItem`,
  `TransitionStatus`, `AddComment`, `GetFieldValue`, `SetFieldValue`
- Implement `JiraIssueTrackerAdapter` wrapping the existing `JiraService`
  - Maps `SearchCriteria` to JQL
  - Maps Jira API responses to `WorkItem`
  - Delegates all operations to the existing `JiraService`
- Existing code and tests remain untouched; this is purely additive

**Testing**

- Adapter correctly maps Jira ticket responses to `WorkItem` fields
- `SearchCriteria` produces correct JQL for various configurations
- Adapter delegates status transitions, comments, field operations correctly
- Error propagation from underlying `JiraService`
- Edge cases: missing fields, security-level redaction

**Documentation**

- Document `IssueTracker` interface contract (godoc)
- Document what is implemented (Jira) vs. planned (GitHub Issues, GitLab)
- Update `docs/architecture-redesign.md` if any design adjustments are needed

**Dependencies**: None (first task)

---

### Task 2: GitService interface evolution

Evolve the existing `GitHubService` interface to match the redesign's
`GitService` contract. Add `SyncWithRemote` for post-commit workspace
reconciliation. The existing interface methods continue to work; this
extends rather than replaces.

**Scope**

- Add `SyncWithRemote(dir, branch string) error` to the `GitHubService`
  interface
  - Implementation: `git fetch origin && git reset --hard origin/<branch>`
  - Preserves untracked files (the key property for artifact persistence)
- Update the mock in `mocks/github.go` to include the new method
- Evaluate whether other `GitService` methods from the redesign should be
  added now or deferred (e.g., existing methods may already satisfy the
  interface -- document the mapping)
- Existing callers are unaffected; `SyncWithRemote` is a new method, not a
  change to existing signatures

**Testing**

- `SyncWithRemote` executes the correct git commands
- Untracked files are preserved after sync (integration test with real git
  repo)
- Error handling: remote unreachable, branch doesn't exist, dirty index
- Existing `GitHubService` tests continue to pass

**Documentation**

- Document the post-commit sync pattern and why it's needed (API commits
  create remote state that local git doesn't know about)
- Document the artifact persistence guarantee (untracked files survive)
- Add godoc to `SyncWithRemote` explaining its role in the workspace
  lifecycle

**Dependencies**: None (independent of Task 1)

---

### Task 3: Workspace Manager

Introduce workspace lifecycle management. Workspaces are scoped to tickets
(not jobs), enabling AI-generated artifacts to persist across container
invocations. This is a new service with no impact on existing code.

**Scope**

- `WorkspaceManager` service with interface:
  - `Create(ticketKey, repoURL string) (string, error)` -- clone repo,
    return workspace path
  - `Find(ticketKey string) (string, bool)` -- find existing workspace
  - `FindOrCreate(ticketKey, repoURL string) (string, bool, error)` --
    return path and whether it was reused
  - `Cleanup(ticketKey string) error` -- remove a specific workspace
  - `CleanupStale(maxAge time.Duration) (int, error)` -- TTL-based cleanup,
    returns count removed
  - `CleanupTerminal(activeTicketKeys []string) (int, error)` -- remove
    workspaces for tickets no longer active
  - `List() ([]WorkspaceInfo, error)` -- list all workspaces (for startup
    scan)
- Directory naming convention: `<base_dir>/<ticket-key>/`
- Configuration additions: `workspaces.base_dir`, `workspaces.ttl_days`
- Config validation: `base_dir` must be set, `ttl_days` must be positive

**Testing**

- Create workspace: directory created, repo cloned
- Find workspace: returns correct path, returns false when not found
- FindOrCreate: creates on first call, reuses on second
- Cleanup: removes directory
- CleanupStale: removes only workspaces older than TTL, preserves recent
- CleanupTerminal: removes workspaces not in active set
- List: discovers workspaces on disk by naming convention
- Error cases: invalid base dir, permission errors, cleanup of non-existent
  workspace

**Documentation**

- Document workspace lifecycle (creation, reuse, cleanup triggers)
- Document artifact persistence convention (untracked files in `.ai-bot/cache/`
  or gitignored directories)
- Document directory structure and naming convention
- Document configuration options

**Dependencies**: None (independent of Tasks 1-2, though will integrate
with `GitService.SyncWithRemote` later in Task 7)

---

### Task 4: ContainerManager -- config resolution and runtime detection

Introduce the container management abstraction. This task covers config
resolution (how the bot discovers what container to use) and runtime
detection (Podman vs Docker). Lifecycle operations (start/stop/exec) are
in Task 5.

The existing `ContainerRunner` from the validation feature branch can be
referenced for runtime detection patterns, but this is a new, broader
abstraction.

**Scope**

- `ContainerManager` interface: `ResolveConfig`, `Start`, `Exec`, `Stop`,
  `CleanupOrphans`
- `ContainerRuntime` interface abstracting Podman vs Docker
- Runtime auto-detection (`exec.LookPath` for podman, then docker)
- `ContainerConfig` type: image, build config, env vars, resource limits,
  mount points
- Config resolution chain:
  1. `.ai-bot/container.json` -- bot-specific config
  2. `.devcontainer/devcontainer.json` -- standard devcontainer (practical
     subset: `image`, `build.dockerfile`, `build.context`,
     `postCreateCommand`, `containerEnv`)
  3. Bot's `container.default_image` config -- admin fallback
  4. Built-in minimal fallback
- Configuration additions: `container.runtime`, `container.default_image`,
  `container.resource_limits.memory`, `container.resource_limits.cpus`
- Parsing logic for devcontainer.json subset (unsupported fields logged
  and ignored)

**Testing**

- Runtime detection: finds podman, falls back to docker, errors when
  neither available
- Config resolution: each priority level, fallback chain
- devcontainer.json parsing: supported fields extracted, unsupported
  fields ignored with log warning
- `.ai-bot/container.json` parsing
- Default image used when no repo config exists
- Config validation

**Documentation**

- Document dev container strategy: how teams configure their environment
- Document config resolution priority and what's read from each format
- Document the "no config" path (minimal fallback, AI generates but can't
  validate)
- Document configuration options

**Dependencies**: None

---

### Task 5: ContainerManager -- lifecycle operations

Implement container start, exec, stop, and orphan cleanup. This is where
containers actually run. Builds on the interface and config resolution
from Task 4.

**Scope**

- `Start`: launch container with resolved config
  - Volume mount: workspace at `/workspace` (`:Z` for SELinux)
  - Environment injection: `AI_PROVIDER`, API keys, `PROJECT_DIR`
  - Resource limits (memory, CPU) via runtime flags
  - Container name prefix for orphan identification
- `Exec`: run a command inside a running container
  - Capture combined stdout/stderr
  - Return exit code
  - Timeout via `context.Context`
- `Stop`: stop and remove a container
- `CleanupOrphans`: find and remove containers matching the bot's name
  prefix
- Output truncation (configurable limit for large outputs)

**Testing**

- Unit tests with mocked `ContainerRuntime`:
  - Start passes correct flags (volume, env, limits, name)
  - Exec captures output and exit code
  - Stop removes container
  - CleanupOrphans filters by prefix
  - Timeout cancellation
- Integration tests with real Podman:
  - Start a container, exec a command, verify output, stop
  - Resource limits applied
  - Volume mount works (file created in container visible on host)
  - Orphan cleanup finds and removes stale containers

**Documentation**

- Document container security model (what's mounted, what's not, no
  GitHub/Jira credentials)
- Document resource limits and timeout behavior
- Document orphan cleanup (naming convention, when it runs)

**Dependencies**: Task 4

---

### Task 6: Task file generation

Implement the mechanism by which the bot communicates goals to the AI.
Instead of prompt templates, the bot writes a structured markdown task
file that the AI reads like a developer reading a task description.

**Scope**

- `TaskFileWriter` service:
  - `WriteNewTicketTask(workItem WorkItem, dir string) error`
  - `WriteFeedbackTask(prDetails PRDetails, comments []PRComment, dir string) error`
- New ticket task file format:
  - Summary, description, acceptance criteria from `WorkItem`
  - Standard instructions ("validate using project tools, don't push to
    git")
- PR feedback task file format:
  - PR context (number, title, branch)
  - Review comments grouped by file, with reviewer attribution
  - Distinction between new comments and already-addressed ones
  - Standard instructions
- `.ai-bot/config.yaml` parsing:
  - `validation_commands` (hints for the AI)
  - `pr` settings (draft, title_prefix, labels -- used by bot)
  - `ai` provider preferences (allowed_tools, model)
- Task files written to `/workspace/.ai-bot/task.md`
- Config file read from `/workspace/.ai-bot/config.yaml`

**Testing**

- New ticket task file: various WorkItem configurations (with/without
  acceptance criteria, security-redacted, different types)
- Feedback task file: single comment, multiple comments, comments on
  different files, general comments, mix of new and addressed
- `.ai-bot/config.yaml` parsing: full config, partial config, missing
  file (defaults), malformed file
- Task file directory creation (`.ai-bot/` may not exist)
- Output format is valid, readable markdown

**Documentation**

- Document task file format with examples (both types)
- Document `.ai-bot/config.yaml` schema with all supported fields
- Document the philosophy: goal-oriented communication, not step-by-step
  instructions
- This replaces the existing prompt templates -- document what's changing
  and why

**Dependencies**: Task 1 (uses `WorkItem` type). Feedback format depends
on PR types from `GitService` (Task 2), but can use local type definitions
if those aren't ready.

---

### Task 7: JobManager

Introduce the central coordination layer. The JobManager receives events,
creates jobs, enforces constraints, and tracks state. This is the
orchestration brain of the new architecture.

**Scope**

- `Job` type: ID, ticket key, job type (new ticket vs feedback), status
  (pending, running, completed, failed), workspace path, retry count,
  timestamps
- `JobManager` service:
  - `Submit(event Event) (*Job, error)` -- create a job from an event
  - `Complete(jobID string, result JobResult) error`
  - `Fail(jobID string, err error) error`
  - `GetJob(jobID string) (*Job, error)`
  - `ActiveJobs() []*Job`
- Deduplication: reject job if one is already running for the same ticket
- Concurrency semaphore: configurable `max_concurrent_jobs`, blocks
  `Submit` when limit reached (or queues and processes when slot opens)
- Retry policy: configurable `max_retries`, exponential backoff
- Workspace integration: delegates to `WorkspaceManager` for
  create/find/cleanup
- Event types: `NewTicketEvent`, `NewFeedbackEvent`
- Configuration: `guardrails.max_concurrent_jobs`, `guardrails.max_retries`

**Testing**

- Submit creates a job with correct initial state
- Deduplication: second submit for same ticket rejected while first is
  running
- Deduplication: submit succeeds after prior job completes
- Concurrency: respects semaphore limit
- Retry: failed job can be resubmitted within retry limit
- Retry: resubmit rejected after max retries exhausted
- Complete/Fail update job state correctly
- ActiveJobs returns only running jobs
- Workspace path assigned from WorkspaceManager

**Documentation**

- Document job lifecycle states and transitions
- Document concurrency model and semaphore behavior
- Document retry policy (count, backoff strategy)
- Document deduplication semantics

**Dependencies**: Task 3 (WorkspaceManager)

---

### Task 8: JobExecutor -- new ticket pipeline

Implement the end-to-end pipeline for processing a new ticket. This is
where all the components come together. The executor handles plumbing;
the AI handles thinking.

**Scope**

- `JobExecutor` service:
  - `ExecuteNewTicket(job *Job) (*JobResult, error)`
- Pipeline steps:
  1. Prepare workspace (WorkspaceManager.FindOrCreate)
  2. Create branch (`{bot-username}/{ticket-key}`)
  3. Write task file (TaskFileWriter.WriteNewTicketTask)
  4. Read `.ai-bot/config.yaml` for container/AI hints
  5. Resolve and start container (ContainerManager)
  6. Launch AI agent inside container, wait for completion or timeout
  7. Collect results: `git diff`, `git status` in workspace
  8. If no changes: return failure (JobManager handles retry)
  9. Commit via GitHub API (verified commit, co-author attribution)
  10. Post-commit sync (GitService.SyncWithRemote)
  11. Generate PR description
  12. Create PR (draft if AI indicated validation failures)
  13. Update ticket: set PR URL, transition to "In Review"
  14. Stop container (workspace retained)
- Error handling: revert ticket status on failure, post error comment
  (unless disabled)
- Draft PR: if AI exits with failure indicators or validation issues,
  create draft PR instead of normal PR; leave ticket in "In Progress"

**Testing**

All dependencies mocked (IssueTracker, GitService, ContainerManager,
WorkspaceManager, TaskFileWriter):

- Happy path: full pipeline succeeds, PR created, ticket transitioned
- AI produces no changes: failure result returned
- AI times out: container killed, failure result, ticket reverted
- Container fails to start: fallback to minimal image, retry
- Commit fails: ticket reverted, error comment posted
- PR creation fails: ticket reverted, error comment posted
- Draft PR path: validation failure indicators trigger draft PR
- Security-level tickets: redacted PR title/body
- Co-author attribution when ticket has assignee
- Error comments disabled: errors logged but not posted to Jira

**Documentation**

- Document the executor pipeline with a step-by-step description
- Document error handling and recovery behavior for each step
- Document draft PR conditions
- Document what the AI CLI command looks like and how the AI discovers
  its task

**Dependencies**: Tasks 1-6 (all foundational components)

---

### Task 9: JobExecutor -- PR feedback pipeline

Implement the pipeline for processing PR review feedback. This is a
variation of the new ticket pipeline with key differences: workspace
reuse, feedback-specific task files, pushing to an existing PR branch,
and replying to review comments.

**Scope**

- `JobExecutor` addition:
  - `ExecuteFeedback(job *Job) (*JobResult, error)`
- Pipeline steps:
  1. Find existing workspace (WorkspaceManager.Find -- must exist)
  2. Sync workspace with remote (GitService.SyncWithRemote -- picks up
     human commits and bot's prior API commits; preserves untracked
     artifacts)
  3. Write feedback task file (TaskFileWriter.WriteFeedbackTask)
  4. Read `.ai-bot/config.yaml` for container/AI hints
  5. Resolve and start container (ContainerManager)
  6. Launch AI agent, wait for completion or timeout
  7. Collect results
  8. If no changes: return failure
  9. Commit via GitHub API (new commit on existing branch)
  10. Post-commit sync
  11. Reply to PR comments that were addressed
  12. Stop container (workspace retained)
- Workspace reuse: verify artifacts from prior sessions are present
- Comment replies: post responses indicating which comments were addressed

**Testing**

All dependencies mocked:

- Happy path: workspace reused, artifacts present, changes committed to
  existing branch, comments replied to
- Workspace not found: error (shouldn't happen in normal flow, but handle
  gracefully)
- Sync picks up human commits: workspace updated before AI runs
- Artifacts survive: untracked files from prior session present after sync
- No changes from AI: failure result
- AI timeout: container killed, failure
- Multiple feedback rounds: workspace reused across several feedback
  cycles
- Comment grouping: comments organized by file in task file

**Documentation**

- Document the feedback pipeline and how it differs from new ticket
- Document workspace reuse and artifact persistence in practice
- Document comment reply behavior
- Document the full lifecycle: ticket created -> bug fix -> PR created ->
  review comments -> feedback processed -> more comments -> more feedback

**Dependencies**: Task 8 (shares `JobExecutor`, extends it)

---

### Task 10: Event-based scanners

Implement the event loop that discovers work. The scanners poll external
systems and emit events to the JobManager. This replaces the existing
scanner implementations with the event-driven model from the redesign.

**Scope**

- `WorkItemScanner`:
  - Polls Jira (via IssueTracker) for tickets in "todo" status
  - Emits `NewTicketEvent` to JobManager
  - Configurable poll interval
- `FeedbackScanner`:
  - Polls Jira for tickets in "in review" status
  - Checks GitHub for new PR comments since last processed timestamp
  - Applies bot-loop-prevention filters:
    - Ignored usernames (completely skipped)
    - Known bot usernames (processed but loop prevention applies)
    - Thread depth limiting
  - Emits `NewFeedbackEvent` to JobManager
- Both scanners are stateless (query each cycle, JobManager handles
  dedup)
- Carry forward existing bot-loop-prevention logic from current codebase
- Event types carry enough context for JobManager to create jobs

**Testing**

- WorkItemScanner: emits events for matching tickets, skips non-matching
- FeedbackScanner: emits events for tickets with new comments
- Bot-loop prevention: ignored users skipped, known bots filtered,
  thread depth respected
- Poll interval respected
- Scanner stop/start lifecycle
- No events emitted when no work found
- Multiple tickets in single scan cycle

**Documentation**

- Document scanner design and event model
- Document bot-loop prevention configuration and behavior
- Document the relationship between scanners and JobManager (scanners
  emit, JobManager deduplicates and schedules)
- Update existing bot-loop-prevention documentation to reflect new
  architecture

**Dependencies**: Tasks 1 (IssueTracker), 7 (JobManager). Task 2
(GitService) for FeedbackScanner's PR comment retrieval.

---

### Task 11: Crash recovery and startup orchestration

Implement the startup sequence that recovers from crashes and cleans up
orphaned resources. The system uses Jira and GitHub as the durable state
store -- no separate database needed.

**Scope**

- Startup sequence:
  1. Clean orphaned containers (ContainerManager.CleanupOrphans)
  2. Scan workspace base directory (WorkspaceManager.List)
  3. Query Jira for tickets in "In Progress" assigned to the bot
  4. For each stuck ticket:
     - Check GitHub for existing PR
     - If no PR: job was interrupted mid-execution, re-queue via
       JobManager
     - If PR exists but ticket still "In Progress": status transition
       was interrupted, complete it
  5. Clean up workspaces for tickets in terminal states
     (WorkspaceManager.CleanupTerminal)
  6. Clean up stale workspaces past TTL
     (WorkspaceManager.CleanupStale)
- Integrate into application startup in `main.go` (runs before scanners
  start)

**Testing**

- No orphans: startup completes cleanly
- Orphaned containers: detected and removed
- Stuck ticket with no PR: re-queued
- Stuck ticket with PR: status transition completed
- Stale workspaces: cleaned up
- Terminal ticket workspaces: cleaned up
- Mixed scenario: combination of the above
- Startup errors are logged but non-fatal (best-effort recovery)

**Documentation**

- Document crash recovery behavior and assumptions
- Document startup sequence order and rationale
- Document what "stuck" means and how it's detected
- Document the durable state model (Jira + GitHub + filesystem)

**Dependencies**: Tasks 3 (WorkspaceManager), 5 (ContainerManager),
7 (JobManager), 1 (IssueTracker)

---

### Task 12: Guardrails, wiring, and cutover

Add cost tracking and budget enforcement. Wire all new components together
in `main.go`. Remove deprecated code paths. Validate the complete system
end-to-end.

**Scope**

- Cost tracking:
  - Track AI session costs (per-job and daily aggregate)
  - Daily budget enforcement: pause job creation when
    `max_daily_cost_usd` exceeded
  - Budget reset (configurable: daily, or manual)
  - Configuration: `guardrails.max_daily_cost_usd`,
    `guardrails.max_container_runtime_minutes`
- Wiring in `main.go`:
  - Initialize all new components with dependency injection
  - Startup sequence (crash recovery → scanners → HTTP server)
  - Graceful shutdown (stop scanners → wait for active jobs → cleanup)
- Cutover:
  - Replace existing scanner/processor pipeline with new
    JobManager/JobExecutor pipeline
  - Remove deprecated code: old `TicketProcessor`, old scanner
    implementations, prompt templates (or mark clearly as deprecated
    with removal timeline if phased cutover is preferred)
  - Remove unused mocks and test helpers
- End-to-end validation:
  - Process a test ticket through the full pipeline
  - Process PR feedback through the full pipeline
  - Verify crash recovery works

**Testing**

- Cost tracking: accumulation, budget check, daily reset
- Budget exceeded: job creation paused, existing jobs finish
- End-to-end (with mocked external services): full ticket lifecycle from
  scan → job → container → AI → commit → PR → feedback → done
- Graceful shutdown: active jobs complete, no orphaned containers

**Documentation**

- Final review of all documentation for accuracy
- Update `CLAUDE.md` project overview to reflect new architecture
- Update `docs/architecture.md` (current system doc) or replace with
  `docs/architecture-redesign.md`
- Update `config.example.yaml` with all new configuration options
- Document migration notes: what changed, what was removed, any
  behavioral differences from the old system
- Remove or archive `plan.md` (pre-PR validation design, superseded)
