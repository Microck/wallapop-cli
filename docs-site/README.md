# docs-site

The documentation site for `wallapop-cli`, built with Fumadocs on Next.js. Intended to be
deployed at `wallapop.micr.dev`; not deployed yet.

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

Wallapop's palette is one accent on black and white: `#13C1AC`, `#000000`, `#FFFFFF`. The site
is dark by default on a true black ground, with the teal used only for interactive elements. In
light mode the accent is darkened to `#0B7D6F`, since the brand teal on white fails contrast at
body size.
