# web/llm

`web/llm` exposes the browser Prompt API under `#web/llm`.

Files:

- `availability`: read-only. Returns `available`, `downloadable`,
  `downloading`, `unavailable`, or an error from `LanguageModel.availability`.
- `chat/`: a small Plan 9-style service directory.
  - `input`: write prompts here.
  - `output`: write a prompt then read the response from the same open file.
  - `status`: same JSON as `status`, useful when `chat` is bound elsewhere.
- `download`: executable and read/write. Explicitly calls
  `LanguageModel.create()` when availability is `downloadable` or
  `downloading`, then destroys the session and reports the new availability.
- `prompt`: executable and read/write. Write one newline-terminated prompt and
  read one newline-terminated response from the same open file.
- `status`: read-only JSON with API presence, availability, user agent, and
  last availability error when available.

The implementation uses `globalThis.LanguageModel.availability()`,
`LanguageModel.create()`, and `session.prompt()`. If the browser does not
provide `LanguageModel`, `availability` reports `unavailable` and `prompt`
returns an error line.

`prompt` calls `LanguageModel.create()` only when availability is exactly
`available`. `downloadable` and `downloading` fail closed; use `download` as the
explicit model setup action.

This is intentionally a small Wanix-facing surface. It does not preserve
conversation sessions between opens, expose sampling options, or stream tokens.
Those can be added as explicit files once the basic contract is useful.

Bind it into a namespace like any other Wanix filesystem:

```html
<wanix-bind dst="llm" src="#web/llm"></wanix-bind>
```

From rc:

```sh
cat web/llm/availability
cat web/llm/status
cat web/llm/download
echo 'Write one sentence about filesystems as APIs.' > prompt.txt
openfile web/llm/prompt < prompt.txt
openfile llm/chat/output < prompt.txt
```
