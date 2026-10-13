# docs-site

The documentation site for `wallapop-cli`, built with Fumadocs on Next.js and exported
to GitHub Pages at `https://wallapop.micr.dev` by `.github/workflows/pages.yml`.

The build copies `scripts/install.sh` to the exported `/install.sh` URL. Keep the
installer in that one source file; the short URL and install examples use it directly.
Search runs in the browser against the generated `/api/search/` index. `/llms.txt`
is also generated during the build, so the published site needs no running server.

```bash
npm ci
npm run dev  # http://localhost:3000/docs
npm run typecheck
npm run build
npm start    # serve the static export at http://localhost:3000/docs/
```

`npm ci` runs `fumadocs-mdx` through `postinstall` to generate the content types before
typechecking or building. CI uses Node.js 22.

## Command reference

`content/docs/reference/*.mdx` is generated from the CLI's cobra tree by `cmd/docsgen`. Do not
edit those pages: change the command's `Short`, `Long` or flag usage in Go and regenerate.

```bash
make docs        # from the repository root
make docs-check  # what CI runs: regenerate and fail if anything moved
```

`content/docs/reference/index.mdx` is written by hand and is left alone by the generator.

## Theme

Brand scale `--brand-50` through `--brand-950` with logo `#14C1AD` at 500. Light
mode is mint (`#F7FCFB` page, `#FFFFFF` cards, `#10201E` text, `#0E9E8E` primary).
Dark mode is deep green (`#071411` page, `#0C1F1B` cards, `#EAFBF8` text,
`#14C1AD` primary, `#74E1D5` focus ring).
