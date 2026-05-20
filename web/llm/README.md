# web/llm

`web/llm` exposes the browser Prompt API under `#web/llm`.

Files:

- `availability`: read-only. Returns `available`, `downloadable`,
  `downloading`, `unavailable`, or an error from `LanguageModel.availability`.
- `prompt`: read/write. Write one newline-terminated prompt and read one
  newline-terminated response from the same open file.

The implementation uses `globalThis.LanguageModel.availability()`,
`LanguageModel.create()`, and `session.prompt()`. If the browser does not
provide `LanguageModel`, `availability` reports `unavailable` and `prompt`
returns an error line.

`prompt` calls `LanguageModel.create()` only when availability is exactly
`available`. `downloadable` and `downloading` fail closed instead of starting
browser model installation from a foreground program.

This is intentionally the smallest Wanix-facing surface. It does not preserve
conversation sessions between opens, expose sampling options, stream tokens, or
download the browser model on behalf of a foreground program. Those can be
added as explicit files once the basic contract is useful.
