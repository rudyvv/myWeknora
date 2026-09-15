# Issue tracker: GitHub

Issues and specs for this repo live as GitHub issues in [`rudyvv/myWeknora`](https://github.com/rudyvv/myWeknora). Use the `gh` CLI with `--repo rudyvv/myWeknora` for all operations.

## Conventions

- **Create an issue**: `gh issue create --repo rudyvv/myWeknora --title "..." --body "..."`. Use a heredoc for multi-line bodies.
- **Read an issue**: `gh issue view <number> --repo rudyvv/myWeknora --comments`, filtering comments by `jq` and also fetching labels.
- **List issues**: `gh issue list --repo rudyvv/myWeknora --state open --json number,title,body,labels,comments --jq '[.[] | {number, title, body, labels: [.labels[].name], comments: [.comments[].body]}]'` with appropriate `--label` and `--state` filters.
- **Comment on an issue**: `gh issue comment <number> --repo rudyvv/myWeknora --body "..."`
- **Apply / remove labels**: `gh issue edit <number> --repo rudyvv/myWeknora --add-label "..."` / `gh issue edit <number> --repo rudyvv/myWeknora --remove-label "..."`
- **Close**: `gh issue close <number> --repo rudyvv/myWeknora --comment "..."`

## Pull requests as a triage surface

**PRs as a request surface: no.** _(Set to `yes` if this repo treats external PRs as feature requests; `/triage` reads this flag.)_

When set to `yes`, PRs run through the same labels and states as issues, using the `gh pr` equivalents:

- **Read a PR**: `gh pr view <number> --repo rudyvv/myWeknora --comments` and `gh pr diff <number> --repo rudyvv/myWeknora` for the diff.
- **List external PRs for triage**: `gh pr list --repo rudyvv/myWeknora --state open --json number,title,body,labels,author,authorAssociation,comments` then keep only `authorAssociation` of `CONTRIBUTOR`, `FIRST_TIME_CONTRIBUTOR`, or `NONE` (drop `OWNER`/`MEMBER`/`COLLABORATOR`).
- **Comment / label / close**: use `gh pr` commands with `--repo rudyvv/myWeknora`.

GitHub shares one number space across issues and PRs, so a bare `#42` may be either: resolve with `gh pr view 42 --repo rudyvv/myWeknora` and fall back to `gh issue view 42 --repo rudyvv/myWeknora`.

## When a skill says "publish to the issue tracker"

Create a GitHub issue in `rudyvv/myWeknora`.

## When a skill says "fetch the relevant ticket"

Run `gh issue view <number> --repo rudyvv/myWeknora --comments`.

## Wayfinding operations

Used by `/wayfinder`. The **map** is a single issue with **child** issues as tickets.

- **Map**: a single issue labelled `wayfinder:map`, holding the Notes / Decisions-so-far / Fog body. Create it with `gh issue create --repo rudyvv/myWeknora --label wayfinder:map`.
- **Child ticket**: an issue linked to the map as a GitHub sub-issue (`gh api` on the sub-issues endpoint). Where sub-issues are unavailable, add the child to a task list in the map body and put `Part of #<map>` at the top of the child body. Labels: `wayfinder:<type>` (`research`/`prototype`/`grilling`/`task`). Once claimed, the ticket is assigned to the driving developer.
- **Blocking**: use GitHub native issue dependencies. Where dependencies are unavailable, fall back to a `Blocked by: #<n>, #<n>` line at the top of the child body. A ticket is unblocked when every blocker is closed.
- **Frontier query**: list the map's open children, drop any with an open blocker or an assignee; first in map order wins.
- **Claim**: `gh issue edit <n> --repo rudyvv/myWeknora --add-assignee @me`, the session's first write.
- **Resolve**: `gh issue comment <n> --repo rudyvv/myWeknora --body "<answer>"`, then `gh issue close <n> --repo rudyvv/myWeknora`, then append a context pointer to the map's Decisions-so-far.
