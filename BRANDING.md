# Questlock brand guidance

The Questlock name and gate artwork identify the upstream project at
[spfuzzylink/questlock](https://github.com/spfuzzylink/questlock), created and
maintained by [@spfuzzylink](https://github.com/spfuzzylink).

## Canonical artwork: reuse unchanged

The canonical project banner is [`assets/questlock-brand.svg`](assets/questlock-brand.svg).
Its exact SHA-256 is:

```text
e8121dd023da8f1d76aa33e633ae03c44fffcc5f7794377e3b2ccb89b145ab29
```

This is the permanent navy, mint and purple hexagon-lock identity. Its mission
is **lower cost per accepted task, including human rework**. The artwork is
versionless and does not advertise a particular release, provider, or feature.
The mission is a project objective, not a claim of measured savings.

Future releases must reuse this exact file and checksum. Do not regenerate,
redesign, retheme, add a version badge, or replace it as part of feature work.
Put release-specific copy in documentation or separate release material.
Changing the canonical identity requires an explicit user request; updating a
checksum test alone is not authorization. Keep the older versioned artwork as
historical material, without rewriting published releases.

The README must reference the canonical asset. `python3 scripts/test_branding.py`
checks both that reference and the frozen bytes. The banner preserves the gate
geometry and palette from `assets/questlock-v0.6.0.svg`; only its approved text
was adapted when the canonical file was established.

This is project guidance for keeping that identity clear. It does not change the
[MIT license](LICENSE), remove its permissions, or retroactively restrict
MIT-licensed code or artwork. It makes no claim of trademark registration.

## Accurate references are welcome

Use the name to accurately describe Questlock, credit its role in your work,
link to the upstream project, or explain compatibility. Examples include
"a client compatible with Questlock" and "built using Questlock."

When showing the gate artwork as a reference to the upstream project, provide
enough context for readers to understand which project it identifies.
[ATTRIBUTION.md](ATTRIBUTION.md) offers an optional credit example and explains
the existing license-notice requirements.

## Make independent work clear

Please clearly identify modified forks and independently operated services so
people can distinguish them from the upstream project. For example:
"Example Service uses a modified Questlock fork and is operated independently."

Please do not falsely present a fork, service, organization, or product as an
official Questlock release or imply upstream affiliation, sponsorship, or
endorsement. Accurate descriptions of what you use or support are welcome.

For background on how names and designs identify the source of goods or
services, see the [USPTO's trademark overview](https://www.uspto.gov/trademarks/basics/what-trademark).
