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

## Lifecycle and timeouts

The host manages several services with `service.Supervisor`:

- A service is loaded on first use and the same runtime is reused; calls run one at a time because a runtime has a single execution thread.
- The resident set is bounded (4 by default), and the least recently used service is evicted past the bound.
- `Sweep` unloads a service that has been idle past a threshold (5 minutes by default), and the next call loads it again from the same bytes.
- One call has a deadline (15 seconds by default). The deadline is enforced by **interrupting the runtime**, not by giving up on the wait: a script is single-threaded and never looks at a context. An interrupted service is unloaded, because a script that is still running cannot be trusted to have left its state consistent.
- A script error is not a broken runtime: the error goes back to the caller and the service stays resident, so the next call uses the same runtime.
- The identity of the manifest the loader returns must match the service that was asked for, or it is refused.

## Transport bridge

A service does not get an `http.Client`; its network goes through `service.Bridge`:

| Rule | Behaviour |
| --- | --- |
| Scheme | only `http` and `https`; `file:`, `ftp:` and the rest are refused |
| Host | a policy may limit requests to a host and its subdomains; empty means any, which only suits a service the user installed by hand |
| Redirects | every hop is checked again by the same rules, at most 5 hops |
| Size | one response is bounded at 4 MiB by default |
| Cookies | none by default; a service is not a browser page |

`Bridge.Host(Host{})` puts the bridge behind the runtime, so a service's `fetch` has no other way out.

## Not implemented yet

- Async handlers that wait: an `async function handle` that settles during the call is read as its result; one that is still waiting is refused clearly instead of being turned into an empty object. `fetch` still blocks, so there is nothing useful to wait for yet.
- Module imports and dependency installs: a service may only use the files in its own package.
- Install, upgrade and interface: Core has the contract alone, and neither client is wired to it.
- Permission prompts: a client should show the requested and refused permissions, and there is no shared interface for that yet.

## Relation to comic and video sources

A service is neither a comic source nor a video source: it does not produce comic or video items, it provides synchronisation, authentication and metadata. In Core the other two live in the `comic/js` and `video` packages, and the three do not share a registry.
