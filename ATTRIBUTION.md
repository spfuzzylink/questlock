# Questlock attribution

Questlock was created and is maintained by
[@spfuzzylink](https://github.com/spfuzzylink), with contributions from the
community. Contributors retain their own copyright interests in their work;
this credit does not assign those interests to the maintainer.

## Required license notices

Questlock's own code remains under the [MIT license](LICENSE). Its copyright
notice and permission notice must be included in copies or substantial portions
of the software. Keep the complete license text when redistributing it.
This document does not change the MIT license or add a new license condition.
See the [Open Source Initiative's MIT text](https://opensource.org/license/mit).

Bundled dependencies and toolchain material retain their applicable terms.
Preserve [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) and any other notices
required by those terms when distributing that material.

## Requested visible credit

If Questlock helps your project, a visible credit and link are appreciated:

> Built with [Questlock](https://github.com/spfuzzylink/questlock), created and
> maintained by [@spfuzzylink](https://github.com/spfuzzylink).

This credit is optional. It is not a condition of using, modifying, distributing,
or selling the MIT-licensed software. No marketing post or public shoutout is
required by this project.

## Inspiration and independent work

OpenRouter's model routing and caching ecosystem inspired Questlock's companion
layer positioning. See the [architecture and source references](docs/model-architecture.md#openrouter-inspiration-and-possible-add-on).
This acknowledgment does not imply a partnership, sponsorship, or endorsement
by OpenRouter or any model provider.

SGLang provides the native inference runtime, HiCache, HiSparse, and the upstream
scheduler/cache simulator described in the runtime guide. Questlock implements
a separate protocol adapter; it does not implement those GPU kernels or claim
the upstream benchmarks as its own. See [SGLang](https://github.com/sgl-project/sglang)
and [the integration boundary](docs/runtime-cache.md).

See [BRANDING.md](BRANDING.md) for guidance on identifying upstream Questlock,
modified forks, and independently operated services.
