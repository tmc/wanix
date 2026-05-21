# web/llm

`web/llm` exposes the browser Prompt API as a Wanix file service under
`#web/llm`.

It follows the usual Wanix shape: control files start and stop work, status
files report state, and stream files carry prompt and response bytes.

```html
<wanix-bind dst="llm" src="#web/llm"></wanix-bind>
```

## Files

Top-level files:

- `availability`: read-only. Prints `available`, `downloadable`,
  `downloading`, `unavailable`, or an availability error.
- `status`: read-only JSON with API presence, availability, download state,
  user agent, Chrome brands when available, and the last availability error.
- `ctl`: write-only global control file.
  - `download`: ask Chrome to create a session with a download monitor.
  - `start`: synonym for `download`.
- `new`: read-only session allocator. Each read creates a session and prints
  its id.
- `prompt`: one-shot prompt file. Write a prompt and read the response from
  the same open file.
- `chat/`: compatibility service directory.
  - `input`: prompt input.
  - `output`: one-shot prompt/response stream, same behavior as `prompt`.
  - `status`: same JSON as top-level `status`.

Session files under `<id>/`:

- `system`: read/write system prompt. Changing it resets the live browser
  session for that Wanix session.
- `prompt`: write-only prompt input.
- `output`: read-only response stream for the most recent prompt.
- `prefill`: read/write assistant response prefix. The next prompt sends it as
  a trailing assistant message with `prefix: true`.
- `schema`: read/write JSON Schema passed as `responseConstraint`.
- `history`: read/write JSON prompt history. Writing replaces the history and
  resets the live browser session.
- `clone`: read-only allocator that copies the session and prints the new id.
- `ctl`: write-only session control file.
  - `stop`: abort the current prompt.
  - `continue`: prompt with `Continue.` in the current session.
  - `close`: abort work, destroy the browser session, close output, and remove
    the Wanix session.
- `status`: read-only JSON with session state, turn count, live-session
  presence, configured option files, and context usage when Chrome exposes it.

## Usage

Check availability and start model setup:

```sh
cat llm/availability
cat llm/status
echo download > llm/ctl
```

Run a one-shot prompt:

```sh
echo 'Write one sentence about how Go and Plan 9 share a philosophy.' > prompt.txt
openfile llm/prompt < prompt.txt
```

One-shot prompts create a browser session for the request and close it after
the response.

## Sessions

Use allocated sessions when you want system prompts, conversation continuation,
history, cloning, structured output, or stop/close controls.

On a fresh page, the first two reads of `new` return `1` and `2`. If sessions
already exist, use the ids printed by `new`.

```sh
cat llm/new
cat llm/new

echo 'You are a terse Go systems programmer.' > llm/1/system
echo 'You are a lyrical Plan 9 guide.' > llm/2/system

cat > prompt-go.txt <<'EOF'
Explain why Go code often favors small interfaces.
EOF
cat prompt-go.txt > llm/1/prompt
cat llm/1/output

cat > prompt-plan9.txt <<'EOF'
Describe namespaces as if introducing Plan 9 to a shell user.
EOF
cat prompt-plan9.txt > llm/2/prompt
cat llm/2/output

cat > prompt-followup.txt <<'EOF'
Continue with one concrete example.
EOF
cat prompt-followup.txt > llm/1/prompt
cat llm/1/output

echo continue > llm/2/ctl
cat llm/2/output

cat llm/1/history
cat llm/1/status
cat llm/1/clone

echo close > llm/1/ctl
echo close > llm/2/ctl
```

Allocated sessions keep a live browser `LanguageModel` session across prompts
until `ctl close`. They also record user and assistant turns in `history`; if a
browser session must be recreated, the recorded turns are supplied as
`initialPrompts`.

`<id>/prompt` is write-only. Do not use:

```sh
openfile llm/1/prompt < prompt.txt
```

Use:

```sh
cat prompt.txt > llm/1/prompt
cat llm/1/output
```

## Structured Output

Write a JSON Schema to `<id>/schema` to pass it as `responseConstraint`:

```sh
cat llm/new
cat > llm/3/schema <<'EOF'
{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}
EOF
echo 'Return a JSON object whose answer says ok.' > llm/3/prompt
cat llm/3/output
echo close > llm/3/ctl
```

## Notes

The implementation uses `globalThis.LanguageModel.availability`,
`LanguageModel.create`, `session.promptStreaming`, and `session.prompt`.

Prompts run only when availability is exactly `available`. `downloadable` and
`downloading` fail closed; write `download` to `ctl` as the explicit setup
action.

Browser-native opaque session persistence across page reloads is not exposed.
Wanix records explicit prompt history only while the page is alive unless a
caller saves and restores `<id>/history`.

Chrome may evict old context internally when the context window overflows.
`<id>/status` reports context fields when available, but does not prevent
overflow.
