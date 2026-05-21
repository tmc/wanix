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
- `system`: read/write global system prompt. It applies to allocated sessions.
- `ctl`: write-only global control file.
  - `download`: ask Chrome to create a session with a download monitor.
  - `start`: synonym for `download`.
- `new`: read-only session allocator. Each read creates a session and prints
  its id.

Prompts run through allocated sessions. Open `<id>/prompt`, write a prompt,
and read the response from the same file. `<id>/output` also exposes the most
recent response as a separate read stream.

Session files under `<id>/`:

- `system`: read/write session system prompt. Changing it resets the live
  browser session for that Wanix session. It is combined with top-level
  `system`.
- `prompt`: prompt/response stream. Open it read/write, write a prompt, and
  read the response from the same open file.
- `output`: read-only response stream for the most recent prompt.
- `prefill`: read/write assistant response prefix. The next prompt sends it as
  a trailing assistant message with `prefix: true`.
- `schema`: read/write JSON Schema passed as `responseConstraint`.
- `history`: read/write JSON prompt history. Writing replaces the history and
  resets the live browser session.
- `context`: read-only JSON prompt context, including the effective system
  prompt followed by user and assistant history.
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

Set an overall system prompt:

```sh
echo 'Answer in at most two short sentences.' > llm/system
cat llm/system
```

Use allocated sessions when you want system prompts, conversation continuation,
history, cloning, structured output, or stop/close controls.

On a fresh page, the first two reads of `new` return `1` and `2`. If sessions
already exist, use the ids printed by `new`.

```sh
cat llm/new
cat llm/new

echo 'You are a terse Go systems programmer.' > llm/1/system
echo 'You are a lyrical Plan 9 guide.' > llm/2/system

echo 'Explain why Go code often favors small interfaces.' > prompt-go.txt
openfile llm/1/prompt < prompt-go.txt

echo 'Describe namespaces as if introducing Plan 9 to a shell user.' > prompt-plan9.txt
openfile llm/2/prompt < prompt-plan9.txt

echo 'Continue with one concrete example.' > prompt-followup.txt
openfile llm/1/prompt < prompt-followup.txt

echo continue > llm/2/ctl
cat llm/2/output

cat llm/1/history
cat llm/1/context
cat llm/1/status
cat llm/1/clone

echo close > llm/1/ctl
echo close > llm/2/ctl
```

Allocated sessions keep a live browser `LanguageModel` session across prompts
until `ctl close`. They also record user and assistant turns in `history`; if a
browser session must be recreated, the recorded turns are supplied as
`initialPrompts`.

System prompts are not stored in `<id>/history`; that file contains user and
assistant turns only. Use `<id>/context` to inspect the effective system prompt
and replayable turns sent to the model.

For separate input and output files:

```sh
cat prompt.txt > llm/1/prompt
cat llm/1/output
```

## Structured Output

Write a JSON Schema to `<id>/schema` to pass it as `responseConstraint`:

```sh
cat llm/new
echo '{"type":"object","properties":{"answer":{"type":"string"}},"required":["answer"],"additionalProperties":false}' > llm/3/schema
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
