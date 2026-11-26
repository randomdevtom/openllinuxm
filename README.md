# openLLinuxM

A terminal coding assistant for **local** models. One static binary, no Python, no cloud.

```
❯ make a hello script
◇ Thought for 3s (ctrl+t to show)
▸ Write(hello.py)
  └─ Wrote 8 lines to hello.py
        1 import sys
        2
        3 def main():
     … +5 lines (ctrl+o to expand)
▸ Update(hello.py)
  └─ Updated hello.py with 1 addition and 1 removal
     -     print('Hello, ' + name)
     +     print(f'Hello, {name}!')
◆ Done. Run it with python3 hello.py Tom.
 ◈ Compiling… (14s · ↓ 312 tokens · esc to interrupt)
╭──────────────────────────────────────────────────────────────────────────────╮
│❯ Ask anything · / for commands                                               │
╰──────────────────────────────────────────────────────────────────────────────╯
  ◆ openLLinuxM  ? shortcuts           Logics-SWE @ Quarry · think on · cache 91%
```

## Features

- **Finds your local server by itself**: Quarry (CPU MoE engine), Ollama,
  llama.cpp, LM Studio, vLLM, Jan, KoboldCpp, or any OpenAI-compatible URL (`--url`).
- **Quarry support**: shows the GGUF it serves, context used, expert-cache hit rate and
  tok/s live in the status bar; Esc cancels the generation on the server too.
- **Native tool calling** (`read_file`, `write_file`, `append_file`, `edit_file`,
  `list_files`, `delete_file`, `run`, `web_search`, `web_fetch`), with a text fallback
  for models that have none. Web search uses DuckDuckGo's HTML page: no API key.
  Edits show as diffs, file writes as previews, command output folded (Ctrl+O expands).
- **Compact prompt**: about 60 words of system prompt plus short tool schemas, so a cold
  start on a CPU model takes seconds, not minutes.
- Streaming replies, Markdown rendering, thinking folded into one live line (Ctrl+T shows
  it), a `/` command menu, input history, multi-line input, mouse scrolling.
- Sends the model's thinking back with each reply, so servers that cache the prompt
  (Quarry, llama.cpp) don't read the conversation again on every step.
- `/goal <task>`: keeps working with the tools until the task is done.
- Shell commands ask first (1 yes / 2 yes, don't ask again / 3 no). Shift+Tab turns
  yolo mode on. Deletes and risky commands always ask.

## Install

```sh
git clone https://github.com/randomdevtom/openllinuxm
cd openllinuxm
make install        # builds and copies to ~/.local/bin/openllinuxm (needs Go 1.24+)
```

## Use

```sh
cd your-project
openllinuxm                     # pick a model the first time; it is remembered
openllinuxm --url http://127.0.0.1:8090 -m quarry
openllinuxm --list              # show local servers and their models
openllinuxm --yolo              # run commands without asking (deletes still ask)
```

| Key / command | |
|---|---|
| Enter / Alt+Enter | send / new line |
| Esc | stop the current reply, or clear the input |
| Shift+Tab | yolo mode (run commands without asking) on or off |
| Ctrl+T | show or hide thinking |
| Ctrl+O | expand or fold tool output |
| `?` | shortcuts |
| `/` | command menu |
| PgUp PgDn, mouse wheel | scroll |
| `/model` | switch server or model |
| `/think on\|off` | reasoning on or off |
| `/goal <task>` | work until done |
| `/cd <dir>`, `/clear`, `/tools`, `/yolo`, `/exit` | |

Code layout: `cmd/openllinuxm` (flags, startup), `internal/llm` (server discovery and
streaming chat), `internal/tools` (the tools), `internal/agent` (the tool loop),
`internal/tui` (the interface), `internal/config`.

Settings live in `~/.config/openllinuxm/config.json`; a log of each session in
`~/.local/state/openllinuxm/log.txt`.

## License

MIT
