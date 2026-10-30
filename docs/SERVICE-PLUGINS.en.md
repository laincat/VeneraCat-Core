# Service plugin contract

[简体中文](./SERVICE-PLUGINS.md) | English

Services such as Trakt, TMDB and Bangumi are **service entries** in the unified collection, not comic sources and not video sources. They share one contract: Core validates and authorises, while each client owns networking, credentials, persistence and the interface.

This document describes the first version of the contract. It covers **manifests, permissions and module calls**; the interface, the install entry and the account flow are up to each client.

## Manifest

A service entry carries a manifest. Core's `service` package parses and validates it:

| Field | Meaning | Rule |
| --- | --- | --- |
| `id` | Identity | lower-case letters, digits, `-` and `.`; unique at a time |
| `name` | Display name | not empty |
| `version` | Version | not empty |
| `kind` | Kind | fixed at `service.js` |
| `entry` | Entry file | relative path inside the package |
| `permissions` | Requested permissions | known permissions only, no duplicates |

Validation is deliberately strict:

- **Unknown fields are refused.** A service that believes it asked for a capability the host ignored would run in a smaller sandbox than it assumes.
- **`entry` must be a relative path inside the package.** Absolute paths, `../`, `./` and backslashes are refused, so a package cannot read a file it does not ship.
- **An unknown permission cannot appear.** When the host and the service disagree about the allow list, there is no way to tell which one is right.

## Permissions

| Permission | Meaning |
| --- | --- |
| `network` | make HTTP requests through the host |
| `storage` | keep a few key-value pairs through the host |
| `secrets` | read the account values the user entered for this service |

The permissions in force are **what the service requests ∩ what the host allows**. The host may allow less: the rest has to show up as denied in the interface rather than silently running with less. `Grants.Effective` lists what is in force and `Grants.Denied` lists what was refused.

A host that allows a permission it does not know is refused, not ignored.

## Module contract

A service is a JavaScript module that exports `handle`:

```js
export function handle(method, payload) {
  if (method === 'ping') return { ok: true };
  return null;
}
```

- `method` is the service's own method name, such as `trakt.sync` or `tmdb.search`.
- `payload` is the raw JSON text of the request; the service parses it.
- The return value is serialised as JSON; `null` means there is no result.

The host calls it through `service.Runtime`: load once, then call per method. Calls are serialised, because a runtime has one execution thread.

## Host capabilities

The module reaches the host through global functions, each gated by a permission. A refused call returns a clear error naming the missing permission, not `undefined is not a function`:

| Global | Permission | Behaviour |
| --- | --- | --- |
| `fetch(url)` | `network` | fetch a response body, synchronously |
| `storageGet(key)` | `storage` | read one key; `null` when unknown |
| `storageSet(key, value)` | `storage` | write one key |
| `secret(key)` | `secrets` | read one account value |
| `log(level, message)` | none | the service's own log lines |

When the host does not implement a capability at all (no network, for instance), the call says so; that is a different thing from a missing permission.

## Not implemented yet

- Async handlers and `await`: the first version supports synchronous handlers only, and `fetch` blocks.
- Module imports and dependency installs: a service may only use the files in its own package.
- Install, upgrade and interface: Core has the contract alone, and neither client is wired to it.
- Permission prompts: a client should show the requested and refused permissions, and there is no shared interface for that yet.

## Relation to comic and video sources

A service is neither a comic source nor a video source: it does not produce comic or video items, it provides synchronisation, authentication and metadata. In Core the other two live in the `comic/js` and `video` packages, and the three do not share a registry.
