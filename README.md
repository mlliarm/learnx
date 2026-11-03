# learnx

Interactive terminal tutorials for any technology, generated from that
technology's own public documentation.

`learnx` connects to a documentation source — ideally a public docs
[MCP](https://modelcontextprotocol.io) server that needs no account — and uses
it to build a curriculum, write lessons grounded in (and citing) the real docs,
and check your exercises against a live sandbox. Elastic Security and
Elasticsearch are supported so far.

> **Status:** early. Docs access, syllabus and lesson generation, sandboxed
> exercise grading, review and the TUI work; LLM features were verified live
> with `openai/gpt-6-luna` on OpenRouter. See [Roadmap](#roadmap).

## Quick start

Requires Go 1.26+.

```sh
go install github.com/mlliarm/learnx/cmd/learnx@latest
```

Or from a checkout:

```sh
go build -o bin/learnx ./cmd/learnx

./bin/learnx packs                         # list tech packs
./bin/learnx docs tools elastic-security   # tools the docs MCP server exposes
./bin/learnx docs search elastic-security "EQL sequence rule" -n 3
```

### Learning

Generating a course needs an LLM. Either:

- **Claude**: set `ANTHROPIC_API_KEY`, or run `ant auth login` once. The model
  is `claude-opus-5-5`.
- **[OpenRouter](https://openrouter.ai)**: set `OPENROUTER_API_KEY`. Any model
  ID of the form `vendor/model` runs on OpenRouter. If only an OpenRouter key is
  set, the model defaults to `openai/gpt-6-luna`.

Override the model with `--model` or `LEARNX_MODEL`. Whichever model you use
needs tool calling and structured outputs.

```sh
learnx plan elastic-security --level beginner \
  --goal "hunt threats with ES|QL and write detection rules" \
  --background "comfortable with SQL and Linux"  # research the docs, write a syllabus
learnx syllabus elastic-security                 # show it; [x] marks written lessons
learnx lesson elastic-security                   # write and show the next lesson
learnx lesson elastic-security 2.1 --solutions   # reread a lesson with its solutions
learnx practice elastic-security                 # do the exercises of the next unfinished lesson
learnx review elastic-security                   # missed exercises, spaced out over time
learnx chat elastic-security "Threshold rule or EQL for SSH brute force?"  # answers from the docs, with links
learnx tui elastic-security                      # all of the above, full-screen
```

`learnx tui` shows the syllabus with your progress. Press enter to read a
lesson (it is written on the spot if needed), `p` to practice it, `r` to
review and `a` to ask the docs. In the query editor, ctrl+s submits and esc skips to the solution.

Stuck? Every exercise has up to three hints, from a gentle nudge to a strong
pointer, that never give the answer away. Press ctrl+g in the TUI, or type `?`
as your answer in `practice` and `review`, to reveal the next one. Hints don't
count as attempts. Exercises from lessons written before hints existed get some
written the first time you ask.

`practice` runs query exercises for real. You type a query, the exercise's
setup runs in a fresh session, and your result is compared with the reference
solution's. Columns are matched by position, so aliases don't matter, and
floats are compared with a small tolerance. Row order only counts when the
reference solution sorts its rows (`| SORT` in ES|QL). Multiple-choice answers are checked exactly, and short answers are graded
by the model against a model answer. Query exercises are also run when a lesson
is written; any that fail are sent back to the model to be fixed. Each pack's
section below describes its sandbox.

Exercises you miss join a review queue. Each correct review pushes an exercise
back by 1, 3, 7, 16 and then 35 days; a miss starts it over.

Syllabi, lessons, attempts and the review queue live in a SQLite database,
`learnx.db`, under `$LEARNX_HOME`, or `$XDG_DATA_HOME/learnx`, or
`~/.local/share/learnx`. JSON files left there by earlier versions are imported
once and kept as a backup.

## How it works

```
┌──────────── TUI (lessons, editor, quiz, chat) ────────────┐
│                     Tutor engine                          │
│   curriculum planner · lesson generator · grader          │
├──────────────┬──────────────────┬─────────────────────────┤
│ DocsSource   │ LLM provider     │ Sandbox (per pack)      │
│  - MCP client│  Claude or       │  - database engine in   │
│  - llms.txt  │  OpenRouter;     │    Docker or locally    │
│  - git repo  │  tool-use loop   │  - runs & checks answers│
│              │  over docs tools │                         │
├──────────────┴──────────────────┴─────────────────────────┤
│ Store: SQLite (lessons, attempts, review queue)           │
└───────────────────────────────────────────────────────────┘
```

- **Docs sources are pluggable.** The `docs.Source` interface (`Search`, `Ask`)
  hides whether docs come from an MCP server, an `llms.txt` file or a docs
  repository.
- **Tech packs** describe each technology: where its docs live, and later how to
  start a sandbox and grade exercises. They are YAML files in [`packs/`](packs/)
  embedded into the binary.
- **Grading prefers ground truth.** Where possible, answers are executed in a
  real sandbox and compared, rather than judged only by an LLM.

### Elastic Security pack

![learnx tui elastic-security: the syllabus, lesson 1.2, a hint, and an ES|QL answer graded against Elasticsearch](docs/images/elastic-security.gif)

| Source | Endpoint |
|---|---|
| Docs MCP (Elastic docs-mcp, no auth) | `https://www.elastic.co/docs/_mcp`: tools `search_docs`, `get_document_by_url`, `find_related_docs` and others |
| llms.txt (fallback) | `https://www.elastic.co/docs/llms.txt` |

The course covers threat hunting and detection engineering: ES|QL hunts,
detection rule types (KQL, EQL, threshold, ES|QL), alert triage and tuning.
Query exercises are ES|QL over ECS-shaped security events, run against a
single-node Elasticsearch 9.4 in Docker with security disabled. The container
(`learnx-elasticsearch`) takes about 30s to start, so it is left running for
later sessions; stop it with `docker rm -f learnx-elasticsearch`. Set
`LEARNX_ES_URL` to use a disposable cluster of your own instead; it is wiped
before every exercise. Exercise setup uses Kibana Dev Tools console syntax:

```console
PUT auth-events
{"mappings": {"properties": {"user.name": {"type": "keyword"}, "source.ip": {"type": "ip"}}}}

POST auth-events/_bulk
{"index": {}}
{"user": {"name": "alice"}, "source": {"ip": "10.0.0.5"}}
```

EQL, KQL and detection rules can't run without Kibana, so lessons teach them
and check them with multiple-choice and short-answer exercises.

### Adding a technology

Create `packs/<name>.yaml`. The Elastic Security pack, for example:

```yaml
name: elastic-security
title: Elastic Security
description: Threat detection, hunting and investigation with Elasticsearch and Elastic Security.
docs:
  mcp:
    endpoint: https://www.elastic.co/docs/_mcp   # public docs MCP server
    search_tool: search_docs                     # tool taking {"query": string}
    base_url: https://www.elastic.co             # optional: resolves "/docs/..." links in results
    # ask_tool: ...                              # optional question-answering tool
  llms_txt: https://www.elastic.co/docs/llms.txt
tutor:
  exercise_env: >-        # tells the lesson writer what exercises can run
    Query exercises run ES|QL against a disposable single-node Elasticsearch
    9.4 cluster ... setup_sql holds Kibana Dev Tools console requests ...
    solution_sql holds one ES|QL query ...
sandbox:
  driver: elasticsearch   # sandbox driver in internal/sandbox
  image: docker.elastic.co/elasticsearch/elasticsearch:9.4.0
```

Everything the LLM needs comes from the docs server's own tools, so a new
technology with a public docs MCP server needs only this file. To run query
exercises it also needs a sandbox driver (`internal/sandbox`). Without a
`sandbox` section, query exercises can't be checked, so `exercise_env` should
ask for multiple-choice and short-answer exercises only.

## Project layout

```
cmd/learnx/        entrypoint
internal/cli/      Cobra commands
internal/docs/     DocsSource interface and MCP implementation
internal/llm/      tool-use loop over the docs tools (Claude or OpenRouter)
internal/tutor/    syllabus planning, lesson writing, exercise checks
internal/sandbox/  Sandbox interface; Elasticsearch driver
internal/grade/    result comparison and answer checking
internal/practice/ answer checking shared by the commands and the TUI
internal/tui/      Bubble Tea TUI
internal/store/    SQLite storage: lessons, attempts, review schedule
internal/pack/     tech pack loading
packs/             built-in tech packs (embedded)
docs/demo/         VHS tapes and record.sh for the README GIFs
```

## Stack

Go, [Cobra](https://github.com/spf13/cobra),
[MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk),
[anthropic-sdk-go](https://github.com/anthropics/anthropic-sdk-go),
[Glamour](https://github.com/charmbracelet/glamour),
[Bubble Tea](https://github.com/charmbracelet/bubbletea),
[Bubbles](https://github.com/charmbracelet/bubbles),
[Lip Gloss](https://github.com/charmbracelet/lipgloss),
[modernc.org/sqlite](https://modernc.org/sqlite).

## Roadmap

1. [x] Connect to a docs MCP server; search and ask from the CLI
2. [x] LLM-generated syllabus and lessons grounded in docs tools
3. [x] Sandbox and execution-based grading (Docker)
4. [x] Bubble Tea TUI; progress stored in SQLite
5. [x] Spaced-repetition review, docs chat with citations

## License

[GNU General Public License v3.0](LICENSE).
