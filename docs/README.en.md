# Documentation

English | [简体中文](README.md)

## Specifications

- [sendMessage interface](SENDMESSAGE-SPEC.en.md): every message of legacy ComicSource scripts and the init.js rewrite scope
- [.venera backup format](VENERA-BACKUP.en.md): the official backup layout, type hashes, and Core's read and restore behaviour
- [Plugins and video sources](VIDEO-PLUGIN-DESIGN.en.md): plugin types, how to write video sources, `vcvideo.json`, authors and homepages
- [How a plugin is distributed](PLUGIN-PACKAGING.en.md): a plugin is one text file today, why it is not an archive yet, and why zip rather than 7z
- [Standard AI API](AIAPI.en.md): built-in OpenAI/Anthropic request construction, authentication headers, native bodies and the cookie rule

## Design records

- [Magpie plugin system](AI-SERVICE.en.md): original provider packages on iOS, independent accounts/runtime and compatibility verification
- [Magpie compatibility](MAGPIE-COMPATIBILITY.en.md): what has actually been run, how it was checked, and what is plainly unsupported
- [JavaScript isolation](JS-ISOLATION.en.md): why each source runs in its own engine
- [Status](STATUS.en.md): what is implemented, verified and pending
- [Code audit](CODE-AUDIT.en.md): cancellation, AI validation and three-project cleanup findings

## Notes

`notes/` holds reference material from experiments, such as the MoeJS feasibility test. It is not part of the specifications.
| [VERIFICATION.en.md](VERIFICATION.en.md) | Verification and evidence: what counts, how to recheck, and three times "verified" was wrong |
| [OPEN-QUESTIONS.en.md](OPEN-QUESTIONS.en.md) | Open decisions: five questions that need a product or security call, and what staying open costs |
| [PROVIDER-TEMPLATE.en.md](PROVIDER-TEMPLATE.en.md) | Writing a provider package: the contract, two templates that run, packaging and self-checks |
