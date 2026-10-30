# Writing a provider package

[简体中文](PROVIDER-TEMPLATE.md) | English

Updated 2026-10-31. This page describes **two packages that run** in this repository, not an
agreement on paper:

- `magpieplugin/testdata/openai-compatible/` — the Chat Completions dialect
- `magpieplugin/testdata/anthropic-compatible/` — the Messages dialect (declares two more things)

`magpieplugin/template_test.go` loads and drives both, so neither can quietly rot.

## What a package provides

```js
export async function Plugin(input) {
  return {
    auth: {
      provider: "my-vendor",              // the id an account is stored under; keep it stable
      methods: [ /* sign-in methods, below */ ],
      async loader(getAuth) {             // how the host sends requests
        const auth = await getAuth()      // what was stored at sign-in
        return {
          baseURL: "https://api.example.com/v1",
          dialect: "chat",                // chat | responses | messages
          headers: { "x-api-key": auth.key }, // optional: the vendor's own header names
          fetch: (url, init) => fetch(url, init),
        }
      },
      // usage and checkin are optional: absent means "the vendor did not report", not zero
    },
  }
}
```

The one detail a key sign-in must get right: **answer with the result directly**

```js
async authorize(inputs) {
  return { type: "success", key: inputs.apiKey, metadata: { baseURL: inputs.baseURL } }
}
```

With `type: "success"` the host does not poll a callback that will never come. `metadata` is what a
loader reads later — a base URL, an account id — and it is stored with the sign-in.

## The dialect decides the path

| `dialect` | default path | for |
|---|---|---|
| `chat` | `/chat/completions` | OpenAI Chat Completions |
| `responses` | `/responses` | OpenAI Responses |
| `messages` | `/messages` | Anthropic Messages |

A vendor with another path returns `path` from its loader. **A package builds no request body and
parses no reply**: the host does both per dialect, and vendor quirks stay in the package.

## Installing it in the client

Put the files side by side and zip them — no extra directory around `package.json`:

```
my-vendor.zip
├── package.json
└── index.mjs
```

On iOS the install screen takes that zip; Core expands it, strips one enclosing directory, refuses
symlinks and paths that escape the archive, and loads the package once to prove it runs — a package
that does not run leaves no record behind.

`TestTemplateSurvivesBeingZipped` is that path as a test: zip the template, expand it, load it, read
the provider.

## Checking your own

```sh
go test -run 'TestTemplate|TestAnthropic' -v ./magpieplugin
```

To add one, copy `testdata/openai-compatible/` to `testdata/<yours>/`, change `provider`, and write a
minimal test like the ones in `template_test.go`. **Make it fail first**: change `auth.provider` to
something else, confirm the test goes red, then put it back — that is what separates "the test
passes" from "the test is not testing".

## What not to do

- Do not `require` or `import` npm modules or Node built-ins (`fs`, `crypto`, `child_process`): the
  loader refuses them by name
- Do not assemble SSE or parse chunks yourself — the host already does, and two implementations mean
  two behaviours
- Do not invent a usage share or a model list when the vendor says nothing: "not reported" and "zero"
  are different things on screen
