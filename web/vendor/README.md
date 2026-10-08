# Vendored browser modules

The UI loads these ES modules directly through the import map in
`web/index.html`; there is no build step. The files are byte-for-byte copies
from the npm packages, so they can be checked against upstream.

| File | Package | Path in package | License |
| --- | --- | --- | --- |
| `preact.mjs` | `preact@11.0.1` | `dist/preact.mjs` | MIT (`LICENSE-preact`) |
| `hooks.mjs` | `preact@11.0.1` | `hooks/dist/hooks.mjs` | MIT (`LICENSE-preact`) |
| `htm.mjs` | `htm@3.1.1` | `dist/htm.module.js` | Apache-2.0 (`LICENSE-htm`) |

## Updating

1. Download the package tarball from the npm registry
   (`https://registry.npmjs.org/<name>/<version>`, field `dist.tarball`).
2. Check it against the registry's `dist.integrity`:
   `echo "sha512-$(openssl dgst -sha512 -binary <file>.tgz | base64)"`.
3. Copy the files listed above and the package `LICENSE`, then update this
   table.

`hooks.mjs` imports `preact` by name; the import map resolves it to
`/vendor/preact.mjs`, so both files must come from the same Preact release.
