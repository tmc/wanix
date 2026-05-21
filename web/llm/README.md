# web/llm

`web/llm` exposes the browser Prompt API under `#web/llm`.

Files:

- `availability`: read-only. Returns `available`, `downloadable`,
  `downloading`, `unavailable`, or an error from `LanguageModel.availability`.
- `chat/`: a small Plan 9-style service directory.
  - `input`: write prompts here.
  - `output`: write a prompt then read the response from the same open file.
  - `status`: same JSON as `status`, useful when `chat` is bound elsewhere.
- `ctl`: write-only control file. Write `download` to call
  `LanguageModel.create()` when availability is `downloadable` or
  `downloading`, monitor browser download progress in the background, then
  destroy the session.
- `new`: read-only allocator. Each read returns a new session id.
- `prompt`: executable and read/write. Write one newline-terminated prompt and
  read the response from the same open file. When the browser supports
  `promptStreaming`, reads return chunks as they arrive.
- `status`: read-only JSON with API presence, availability, download state,
  user agent, and last availability error when available.
- `<id>/`: a session directory allocated by `new`.
  - `system`: read/write system prompt used when creating browser sessions.
  - `prompt`: write prompts here.
  - `output`: read streamed response bytes.
  - `prefill`: read/write assistant response prefix. The next prompt sends it
    as a trailing assistant message with `prefix: true`.
  - `schema`: read/write JSON Schema passed as `responseConstraint`.
  - `history`: read/write JSON prompt history used for restore.
  - `clone`: read to allocate a copy of this session's prompts and history.
  - `ctl`: write `stop`, `continue`, or `close`.
  - `status`: read session state, including context usage when the browser
    exposes it.

The implementation uses `globalThis.LanguageModel.availability()`,
`LanguageModel.create()`, `session.promptStreaming()`, and `session.prompt()`.
If the browser does not provide `LanguageModel`, `availability` reports
`unavailable` and `prompt` returns an error line.

`prompt` calls `LanguageModel.create()` only when availability is exactly
`available`. `downloadable` and `downloading` fail closed; write `download` to
`ctl` as the explicit model setup action.

Allocated sessions keep a live browser `LanguageModel` session across prompts
until `ctl close`. Long-term state is also represented as stored user and
assistant turns in `history`, so a session can be restored with
`initialPrompts` when a live browser session must be recreated. Browser-native
opaque session persistence across page reloads is not exposed.

Bind it into a namespace like any other Wanix filesystem:

```html
<wanix-bind dst="llm" src="#web/llm"></wanix-bind>
```

From rc:

```sh
cat web/llm/availability
cat web/llm/status
echo download > web/llm/ctl
echo 'Write one sentence about how Go and Plan 9 share a philosophy.' > prompt.txt
openfile web/llm/prompt < prompt.txt
openfile llm/chat/output < prompt.txt
# On a fresh page, these reads return 1 and 2.
cat llm/new
cat llm/new
echo 'You are a terse Go systems programmer.' > llm/1/system
echo 'You are a lyrical Plan 9 guide.' > llm/2/system
echo 'Explain why Go code often favors small interfaces.' > llm/1/prompt
cat llm/1/output
echo 'Describe namespaces as if introducing Plan 9 to a shell user.' > llm/2/prompt
cat llm/2/output
echo continue > llm/1/ctl
cat llm/1/output
cat llm/1/history
cat llm/1/clone
echo close > llm/1/ctl
echo close > llm/2/ctl
```

## Streaming design

The native streaming primitive in Wanix is a pipe. `fs/pipe` exposes two stream
files, `data` and `data1`, backed by connected ports. Reads block until bytes
arrive when the pipe is created in blocking mode. That matches browser
`promptStreaming`: the model writes chunks, and the consumer reads bytes until
EOF.

The Plan 9 plumber is a message router, not the byte stream itself. Plumbing
rules decide where a prompt or selection should go. The response should then be
read from a stream file, such as a pipe-backed `out`, rather than from a control
file or a file whose read operation starts work.

The LLM filesystem separates commands, status, and streams:

```text
llm/
  new          read allocates a session id
  ctl          global commands, such as download
  status       global status
  <id>/
    system     read/write system prompt
    prefill    read/write assistant prefix for the next response
    schema     read/write JSON Schema response constraint
    history    read/write JSON prompt history
    clone      read to allocate a forked session
    ctl        session commands: stop, continue, close
    prompt     write prompt text
    output     read streamed response bytes
    status     session status
```

`prompt` and `chat/output` are convenience files for the common one-shot case.
They should behave as stream files: writing a prompt starts work, and reads
return response chunks as the browser produces them. Control operations remain
writes to `ctl`, and status remains reads from `status`.
