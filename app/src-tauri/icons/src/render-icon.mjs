// Regenerates app/src-tauri/icons/*.png from keld-k-small-white.svg — the same
// mark Atlas serves at services/web/public/keld-k-small-white.svg, kept here as
// a copy so this repo can rebuild its own icon without the other checked out.
//
// Run from anywhere:  node app/src-tauri/icons/src/render-icon.mjs
//
// Playwright rather than rsvg/inkscape/ImageMagick because none of those is a
// dependency of this repo and the browser is already one — it renders the SVG
// exactly as the page would, so the icon cannot drift from the mark.
// ⚠️ Playwright is resolved from ui/e2e, not from here. ESM resolves a package
// relative to the IMPORTING FILE, so a bare `import "playwright"` in this
// directory fails however the script is invoked — the install lives with the
// e2e suite and this repo has no other node_modules.
import { createRequire } from "node:module";
import fs from "node:fs";
const require = createRequire(new URL("../../../../ui/e2e/package.json", import.meta.url));
const { chromium } = require("playwright");

const svg = fs.readFileSync(new URL("./keld-k-small-white.svg", import.meta.url), "utf8")
  .replace(/^<\?xml[^>]*\?>\s*/, "");

// macOS app-icon geometry: a squircle-ish rounded square, artwork inset so it
// is not crowded by the corners. 22.37% radius is the Big Sur ratio; the K sits
// at ~52% of the canvas, centred on its own bounding box.
const page = (size, radius, inset) => `<!doctype html>
<html><head><meta charset="utf-8"><style>
  html,body{margin:0;padding:0;background:transparent}
  .icon{width:${size}px;height:${size}px;background:#000;border-radius:${radius}px;
        display:flex;align-items:center;justify-content:center}
  .icon svg{width:${inset}px;height:auto;display:block}
</style></head><body><div class="icon">${svg}</div></body></html>`;

const browser = await chromium.launch();
for (const [name, size] of [["icon.png", 512], ["128x128.png", 128], ["32x32.png", 32]]) {
  const p = await browser.newPage({ viewport: { width: size, height: size }, deviceScaleFactor: 1 });
  await p.setContent(page(size, Math.round(size * 0.2237), Math.round(size * 0.52)));
  await p.locator(".icon").screenshot({ path: new URL(`../${name}`, import.meta.url).pathname, omitBackground: true });
  await p.close();
}
await browser.close();
console.log("rendered");
