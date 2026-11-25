# openLLinuxM

A terminal coding assistant for **local** models. One static binary, no Python, no cloud.

```
 ◆ openLLinuxM                                  think off · Qwen3.8-35B-A3B-Q3_K_M @ Quarry
  ┃ create hi.txt containing hi, then say done
  ✓ write_file hi.txt  (1 lines)
  │ wrote 1 lines
    done
╭─────────────────────────────────────────────────────────────────────────────────────────╮
│❯ Ask anything · /help for commands                                                      │
╰─────────────────────────────────────────────────────────────────────────────────────────╯
  enter send  alt+enter newline  ctrl+t thinking  /help          ctx 846/32.8k · cache 88%
```

## Features

- **Finds your local server by itself**: Quarry (CPU MoE engine), Ollama,
  llama.cpp, LM Studio, vLLM, Jan, KoboldCpp, or any OpenAI-compatible URL (`--url`).
- **Quarry support**: shows the GGUF it serves, context used, expert-cache hit rate and
  tok/s live in the status bar; Esc cancels the generation on the server too.
- **Native tool calling** (`read_file`, `write_file`, `edit_file`, `list_files`,
  `delete_file`, `run`), with a text fallback for models that have none.
- **Compact prompt**: about 60 words of system prompt plus short tool schemas, so a cold
  start on a CPU model takes seconds, not minutes.
- Streaming replies, Markdown rendering, collapsible reasoning (Ctrl+T), input history,
  multi-line input, mouse scrolling.
- `/goal <task>`: keeps working with the tools until the task is done.
- Shell commands ask first (`y` / `n` / `a` = always). Deletes and risky commands always ask.

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
| Esc | stop the current reply |
| Ctrl+T | show or hide reasoning |
| PgUp PgDn, mouse wheel | scroll |
| `/model` | switch server or model |
| `/think on\|off` | reasoning on or off |
| `/goal <task>` | work until done |
| `/cd <dir>`, `/clear`, `/tools`, `/yolo`, `/exit` | |

Settings live in `~/.config/openllinuxm/config.json`; a log of each session in
`~/.local/state/openllinuxm/log.txt`.

## License

MIT
