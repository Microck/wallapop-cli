# docs-site

The documentation site for `wallapop-cli`, built with Fumadocs on Next.js. Deployed on Vercel
as `wallapop-cli`; the `wallapop.micr.dev` DNS record (Netlify NS1) is still missing.

```bash
npm ci
npm run dev  # http://localhost:3000/docs
npm run typecheck
npm run build
npm start    # serve the production build at http://localhost:3000/docs
```

`npm ci` runs `fumadocs-mdx` through `postinstall` to generate the content types before
typechecking or building. Node.js 22 is used in CI.

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
