# JavaScript isolation

English | [简体中文](JS-ISOLATION.md)

Design record, 2026-10-29.

## Problem

A review, confirmed by a reproduction, found that all sources shared one MoeJS runtime. The cookie and clearance partition came from a global `activeKey`, and the storage key came from the `key` field the script itself put into the message. A source could therefore read another source's cookies and stored data, even from its constructor (regression test: `TestCrossSourceIsolation`). `activeKey` could also already belong to another source after an `await`, and concurrent `LoadSource` calls shared `globalThis.__vcPending`.

## Solution: one engine per source

- `Runtime` became a manager holding `map[key]*engine`. Each `engine` has its own `moejs.Runtime`, event loop, `html.Store` and `image.Store`, and is bound to one key.
- `sendMessage` is a method of the engine. Partitions, storage and settings always use the engine's bound key; the `key` field in a message is ignored.
- `LoadSource` first evaluates the script and reads its metadata in a new, unbound engine, and registers it under the key only after validation; on failure the engine is closed. While the unbound engine runs, its network requests use their own partition and its storage is an in-memory scratch area, so it cannot reach any installed source's data. Writes made while loading are copied to the real storage once the source is registered.
- The public API (`New`, `LoadSource`, `Call`, `Has`, `Unload`, `Sources`, `Eval`, `VideoProvider`, `Close`) kept its signatures. `Eval` runs on a "global" engine that belongs to no source (for tests and debugging).
- `runAs` and `activeKey` were removed.

Cost: about 0.9 ms to load and 112 KiB of memory per source (Windows measurement).

## Fixed at the same time (review findings)

- UI callbacks deadlocked when fired synchronously on the loop; client UI handlers now never run on the loop.
- `Close` cancels host work before taking the lock, so it does not wait for a timeout.
- Script `http` uses the call's context instead of the runtime's, so `Cancel(callID)` reaches the client.
- Video play results are validated: http(s) URLs only and well-formed headers.
