# Documentation

English | [简体中文](README.md)

## Specifications

- [sendMessage interface](SENDMESSAGE-SPEC.en.md): every message of legacy ComicSource scripts and the init.js rewrite scope
- [.venera backup format](VENERA-BACKUP.en.md): the official backup layout, type hashes, and Core's read and restore behaviour
- [Plugins and video sources](VIDEO-PLUGIN-DESIGN.en.md): plugin types, how to write video sources, `vcvideo.json`, authors and homepages
- [How a plugin is distributed](PLUGIN-PACKAGING.en.md): a plugin is one text file today, why it is not an archive yet, and why zip rather than 7z
- [Service plugin contract](SERVICE-PLUGINS.en.md): the manifest, permissions, module `handle` contract and host capabilities of a service entry
- [Standard AI API](AIAPI.en.md): built-in OpenAI/Anthropic request construction, authentication headers, native bodies and the cookie rule

## Design records

- [Magpie plugin system](AI-SERVICE.en.md): original provider packages on iOS, independent accounts/runtime and compatibility verification
- [Magpie compatibility](MAGPIE-COMPATIBILITY.en.md): what has actually been run, how it was checked, and what is plainly unsupported
- [JavaScript isolation](JS-ISOLATION.en.md): why each source runs in its own engine
- [Status](STATUS.en.md) maps the current architecture, compatibility limits, verification and pending work.

## Notes

- The [verification guide](VERIFICATION.en.md) explains how to reproduce checks and distinguish evidence across environments.
- [Open questions](OPEN-QUESTIONS.en.md) records decisions that still need product input.
- [Provider templates](PROVIDER-TEMPLATE.en.md) provides runnable examples, packaging and self-checks.
- [GBK compatibility](GBK-COMPATIBILITY.en.md) records differences in valid characters, invalid bytes and private-use mappings.
