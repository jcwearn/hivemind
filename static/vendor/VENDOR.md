# Vendored assets

Served from this binary rather than a CDN, so the program has no network
dependencies and a room with bad wifi is not one CDN timeout away from an
unusable controller.

The version is in the filename, which is what lets these be served with
`Cache-Control: immutable` — a new version is a new path.

| File | Source | SHA-384 |
|---|---|---|
| `htmx-2.0.10.min.js` | https://unpkg.com/htmx.org@2.0.10/dist/htmx.min.js | `H5SrcfygHmAuTDZphMHqBJLc3FhssKjG7w/CeCpFReSfwBWDTKpkzPP8c+cLsK+V` |
| `htmx-ext-sse-2.2.4.js` | https://unpkg.com/htmx-ext-sse@2.2.4/sse.js | `QA9wXqexhwzXTuTvuF5QP82pddm3R2hy81UzXi7ioNTqNF2b75hlkkSGjafohhL3` |

To verify:

```bash
openssl dgst -sha384 -binary static/vendor/htmx-2.0.10.min.js | openssl base64 -A
```

To update, see `make vendor-htmx`. Renovate deliberately does not manage these —
a regex manager can rewrite a version string but cannot rename a file, so every
bump it opened would be a broken build.
