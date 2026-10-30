# Open decisions

[简体中文](OPEN-QUESTIONS.md) | English

Updated 2026-11-01. Only the questions that need a product or security decision appear here, with
what happens if they stay open. Work that is merely unfinished does not belong on this page.

## 1. ATS policy on iOS

**Now**: the client configures **nothing** for ATS — `project.yml` sets `GENERATE_INFOPLIST_FILE: YES`
and no `NSAppTransportSecurity` key — so the system default applies: HTTPS only, plus the few
exceptions iOS ships. No site was blocked in the real-source runs **because every verified source
uses https**; that is not evidence that an http source works, and today it would fail with an ATS
message from the system rather than anything the app can explain.

**Why you**: `NSAllowsArbitraryLoads` is the easy switch and it removes the system's floor from
*every* request the app makes, including the conversation with an AI provider. Per-host exceptions
keep that floor but need a release whenever a new source appears.

**Options**: A. allow everything; B. exceptions for a maintained list of comic hosts; C. keep the
current state and evaluate when something is actually blocked. **If it stays open**: a user meets an
http source and what they see is "no images", not a sentence they can act on.

## 2. Migrating cookies from a global jar into per-source partitions

**Now**: every source has its own partition, which is part of the security design. A `.venera`
backup carries global cookies, and importing one currently lands sources and shelf data while
**leaving cookies behind**.

**Why you**: migrating means copying one global credential into several partitions — widening where
a credential is visible. Doing it by default and not doing it both cost something.

**Decided (2026-10-31, the user chose this direction)**: Core names the target partitions and the
client writes them; no shared jar is rebuilt.

- `backup.CookieTargets(backupSources, installed)`: the sources the backup carries, because the
  import installs them. "Installed already" cannot be the condition — a new device has nothing, and
  that is the migration case. No source scripts means every installed partition.
- Distribution itself is the client's: each target partition is written separately, under that
  client's own partition name (`comic:<key>` on both iOS and desktop). No per-source filtering,
  because a jar only sends a cookie to a matching domain, so handing one to a source that never
  visits that host is inert.
- `PlanBackup` therefore also returns `cookieTargets`; the client writes the cookies into its own
  partitions and no global jar returns.
- Three kinds of row are dropped: no name, no value, and a domain that is not a host (including
  junk such as `https://` written into the column).

**Still open is only this**: whether an import *ticks* cookies by default. Core's default selection
still leaves them out because they are sensitive; making the client default to on is a product call.

## 3. Video playback architecture (Windows/Linux)

**Now**: Core parses video sources and HLS; the desktop only wired the reader, and
`browser.Sniffer` returns "unsupported" on purpose.

**Why you**: playback is either a WebView (the same machinery as the reader, with its limits) or a
native player per platform (double the work, different codec and DRM problems on each).

**Options**: A. WebView plus `<video>`; B. a native player per platform; C. only sniff a direct URL
and hand it to the system player. **If it stays open**: video sources install and search, and
tapping one does nothing.

## 4. Where provider packages come from

**Now**: the iOS client installs a **folder or a zip/tar.gz**; Core ships no provider package. Of
fifteen sampled community packages, only the original Copilot and `cloudflare-workers-ai` run — the
rest need a Node runtime.

**Why you**: shipping one or two pure-Web reference packages (an OpenAI-compatible one, an
Anthropic-compatible one) decides whether the app has anything to do once it is installed.

**Options**: A. publish reference packages with Core and bundle them; B. documentation and a
template only; C. work with the community on a list of packages that actually run. **If it stays
open**: a fresh install is empty, and the AI feature only works for someone who finds and installs a
third-party package themselves.

## 6. What `enableTagsTranslate` means

**Where it stands**: four sources declare `enableTagsTranslate: true` (`ehentai`, `hitomi`, `lanraragi`, `nhentai`) and several declare `false`. **No client has ever read the field**, so tags are shown exactly as the source writes them. Nothing in this repository defines its semantics: the source scripts do not explain it, and neither the Core docs nor `init.js` implement it.

**Why it needs you**: at least two readings are defensible, with different costs —
A. **translate tags with the source's own `translation` table**: implementable today, but that table mostly carries **group names** (`Tags` → `标签`), not tag values, so coverage would be thin;
B. **bring in a tag dictionary** (the kind of tag → local-language table Venera ships upstream): far better coverage, but it means deciding **where the dictionary comes from, who maintains it, whether it ships with the app**, and what to show when an entry is missing.

Both also force a second decision: **display only, or search too** (translating a Chinese tag back to English before searching changes the keyword the source receives).

**Cost of not deciding**: nothing happens — users see the source's own tag text. Nothing breaks; it is simply not translated.

## 5. Verifying against a real vendor account

**Now**: device code, key sign-in, refresh, usage, models, streaming and cancellation are all
tested in Core, and exercised with the **real Copilot package** against a fake service. On the
client, the sign-in UI, Keychain and transport pass the macOS type checks and unit tests.

**What is missing**: one end-to-end run with a real account on a real device. That needs an account
from you, or you tapping through it once. **If it stays open**: the honest claim is "implemented",
not "works"; and a community package's real behaviour — rate limits, second factors, regional
restrictions — stays unknown.

## 7. `magpieplugin.Sender` has no production caller yet

**Where it stands**: `Sender` — endpoint walking, the retry decision, the `Pin` — has a full set of tests, and **nothing in this repository calls it in production**: `mobile` chats through the plugin package's own `Host.HTTP`, and the desktop talks to the Magpie desktop app rather than through `magpieplugin`. So the retry decision and the pin are, today, a **library surface for a host**, not a live protection.

**Why it needs a decision**: three directions, with different costs —
A. **Let `mobile` use `Sender` when a package offers several endpoints** (mirrors or a second account); this changes the package contract;
B. **Use it inside the desktop's Magpie gateway**, which does route — that work lands in the desktop repository;
C. **Keep it as a library surface and say so**, which is what the documentation does now.

**If left open**: nothing breaks, but it would be wrong to claim retries are constrained — that is code with tests, not code in the path.
