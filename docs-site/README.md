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

The tokens follow Wallapop's own dark theme: page ground `#0B0B0C`, sidebar `#151617`,
text `#FBFBF9`, dimmed text `#8E8E8B`, accent blue `#385EF9`. Light mode uses the
light-side tokens (`#FFFFFF` ground, `#29363D` text, `#0B2DB0` dark blue accent).
