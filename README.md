# cu — a fast terminal UI for ClickUp

A lazygit-style TUI for ClickUp, written in Go on [jesseduffield/gocui](https://github.com/jesseduffield/gocui),
the same UI library lazygit is built on.

![cu: lists, tasks with coloured custom fields, pinned tasks and the task panel](docs/screenshot.png)

**Why it's fast:** every view renders instantly from a local SQLite cache and syncs in the
background (stale-while-revalidate). Edits are optimistic: the UI updates immediately and rolls
back if ClickUp rejects the change. The workspace tree loads concurrently, scrolling through
tasks only fetches the one you stop on, and filtering happens in memory.

## Install

Needs Go 1.26 or newer.

```sh
go install codeberg.org/b-wisman/clickup-tui/cmd/cu@latest
```

or from a checkout: `go build -o bin/cu ./cmd/cu`.

## Setup

Create a personal API token in ClickUp (avatar → **Settings** → **Apps** → **API Token**) and
either export it:

```sh
export CLICKUP_API_TOKEN=pk_...
export CLICKUP_TEAM_ID=1234567   # optional: which workspace to open (default: the first)
```

or put it in `~/.config/clickup-tui/config.toml`:

```toml
token = "pk_..."
team_id = "1234567"   # optional
```

The cache lives in `~/.cache/clickup-tui/cache-go.db` (both paths follow `XDG_CONFIG_HOME` and
`XDG_CACHE_HOME`). It's only a cache: delete it any time.

## Usage

```sh
cu        # open the TUI
cu demo   # try it offline, against a built-in demo project
cu seed   # create the demo project in your workspace: a list with coloured
          # Scope/Severity fields and 12 tasks. Safe to re-run.
          # Options: -space "Team Space" -list "clickup-tui demo"
```

## Panels

Like lazygit, the screen is a set of numbered panels; the focused one has a green border and the
bottom line shows its most useful keys. `?` lists every key of the focused panel, and pressing a
key in that list runs it.

| Panel | |
|---|---|
| `[1]` Workspace | workspace, user, sync state and the running timer |
| `[2]` Lists | spaces, folders and lists |
| `[3]` Tasks | the open list, or `Mine` (tasks assigned to you); switch with `[` `]` |
| `[4]` Pinned | tasks you pinned, from any list; kept between starts |
| `[0]` Task | the selected task: fields, description, subtasks, comments |
| Command log | every API call with its status and latency (`@` toggles it) |

## Keys

**Everywhere**

| Key | |
|---|---|
| `1` `2` `3` `4` · `0` | focus a panel · the task panel |
| `h` `l` · `←` `→` · `tab` | previous / next panel |
| `J` `K` · `ctrl+d` `ctrl+u` | scroll the task panel from anywhere |
| `?` · `x` | keybindings of the focused panel |
| `ctrl+p` | jump to any list (type to filter) |
| `g` | go to a task by id, custom id (`DEV-123`) or URL |
| `W` | timesheet (see below) |
| `+` `_` | screen mode: normal → half → full |
| `@` | toggle the command log |
| `R` · `q` | refresh · quit |

**Tasks, Pinned and the task panel**

| Key | |
|---|---|
| `j` `k` · `<` `>` · `,` `.` | move · top / bottom · page (Tasks) |
| `enter` · `esc` | open the task panel · back to the list |
| `space` | status menu with the next status preselected: `space` `enter` moves a task along, numbers jump |
| `s` | find a status by typing |
| `f` | custom fields: every field has a hotkey, dropdown options are numbered, so `f` `e` `2` sets a field |
| `A` · `a` | assign anyone (type a name, `enter` toggles) · assign or unassign me |
| `p` · `t` | priority · due date (`today`, `tomorrow`, `+3d`, `fri`, `10-31`, `2026-10-31`, `none`) |
| `n` · `N` | new task · new subtask (see below) |
| `r` · `e` | rename · edit the description in `$EDITOR` |
| `c` · `C` | comment · comment in `$EDITOR` |
| `L` | log time in one line: `1h30`, `45m fixed login`, `2h yesterday`, `1:30 mon 09:00 review` |
| `T` · `w` | start or stop a timer · the task's time entries (pick one to edit; clearing it deletes it) |
| `P` | pin / unpin |
| `d` · `o` · `y` | delete (asks first) · open in the browser · copy URL, id, name or a markdown link |
| `/` · `v` | filter by name, id, status, assignee, tag or dropdown value · show closed tasks (Tasks) |

**Lists:** `enter` opens a list, `space` expands or collapses, `/` searches all lists.
**Workspace:** `enter` switches workspace.

Durations can be written `1:30`, `1h30`, `1h 30m`, `90m`, `1.5` or `2h`. Day words look back:
`mon` means the last Monday. Without a day or start time, a logged entry ends now.

Dropdown custom fields show as coloured chip columns in the task list when there's room.

### New tasks

`n` opens a form: type the name, and below it set the status, assignees, priority, due date and
every custom field of the list. Required fields are listed first, marked `*`. `enter` creates the
task, and first walks you through any required field that is still empty, so for a list with a
required Severity it's: type a name, `enter`, `2`, done. `tab` moves between the name and the
fields (`j` `k` move, `enter` edits), `ctrl+s` creates from anywhere and `esc` goes back.

### Timesheet

![the timesheet: a week of time per task and day](docs/timesheet.png)

`W` opens a week of your time per task and day, like ClickUp's web timesheet. The right side
shows the entries behind the selected cell.

| Key | |
|---|---|
| `h` `j` `k` `l` | move between days and tasks |
| `enter` | edit the cell: type hours like `2:15`, or `1:30 09:00 review` to set a start and note; empty or `0` deletes. A cell with several entries lets you pick one |
| `a` · `d` | add a task row (from pinned and visible tasks) · clear the cell |
| `[` `]` · `t` | previous / next week · this week |
| `esc` | back |

Time tracking needs ClickUp's *Time Tracking* ClickApp enabled in the workspace.

### Mouse

Click a panel to focus it and a row to select it; double-click a task to open it, or a timesheet
cell to edit it. Click the `List` / `Mine` tab to switch, scroll with the wheel, and click an
option in a popup to pick it.

## Code

```
cmd/cu            entry point: cu, cu demo, cu seed
internal/clickup  API client: typed errors, retries honouring rate limits, page iterators
internal/cache    generic SQLite cache; stale-while-revalidate as an iterator
internal/app      state and actions, independent of the terminal; tested with a scripted UI
internal/gui      gocui panels, popups, keymap and mouse; tested headless, key by key
internal/render   pure formatting: task table, task panel, custom fields, timesheet, markdown subset
internal/style    ANSI styling, ClickUp colour chips with readable text
internal/parse    user input: task references, due dates, durations, one-line time logs
internal/fake     in-memory ClickUp API for the tests and `cu demo`
internal/seed     the demo project
```

## Development

```sh
go test -race ./...
go run ./cmd/cu demo
```
