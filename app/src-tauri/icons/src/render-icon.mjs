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
const appPage = (size, radius, inset) => `<!doctype html>
<html><head><meta charset="utf-8"><style>
  html,body{margin:0;padding:0;background:transparent}
  .icon{width:${size}px;height:${size}px;background:#000;border-radius:${radius}px;
        display:flex;align-items:center;justify-content:center}
  .icon svg{width:${inset}px;height:auto;display:block}
</style></head><body><div class="icon">${svg}</div></body></html>`;

// The tray icon is the SAME mark with none of that chrome: no black plate and
// no corner radius. A macOS menu-bar icon is a TEMPLATE image — the system
// reads only the alpha shape and paints it itself, black on a light menu bar
// and white on a dark one — so a rounded black square would render as a solid
// blob and the artwork's own colour is never used. Drawn in black anyway
// (`fill`/`stroke` forced with CSS, rather than editing the source SVG's white,
// so this does not depend on how the mark spells its colour) and the file then
// still reads correctly anywhere the template flag does not apply.
const trayPage = (w, h, artW, artH) => `<!doctype html>
<html><head><meta charset="utf-8"><style>
  html,body{margin:0;padding:0;background:transparent}
  .tray{width:${w}px;height:${h}px;display:flex;align-items:center;justify-content:center}
  .tray svg{width:${artW}px;height:${artH}px;display:block}
  .tray svg line{stroke:#000 !important}
  .tray svg path{fill:#000 !important}
</style></head><body><div class="tray">${svg}</div></body></html>`;

// 44px is 22pt at @2x — the menu bar's own height, and the canvas size the
// previous tray icon used. ⚠️ The ARTWORK is 36px inside it, not 44: the mark's
// vertical stroke touches its own viewBox top and bottom (as does the app
// icon's, which is why that one is inset to 52% of its plate), so rendering it
// full-bleed puts a glyph in the menu bar noticeably taller than the clock and
// battery beside it. 36/44 is ~18pt of 22pt, Apple's menu-bar extra guidance,
// and checked against the system items at menu-bar scale. Width follows the
// mark's own aspect rather than being padded to a square, which is what keeps
// it from reading as a featureless block; macOS adds its own horizontal
// padding around a status item, so none is baked in here.
const TRAY_H = 44;
const TRAY_ART_H = 36;
const TRAY_ART_W = Math.round(TRAY_ART_H * (160.92939 / 150));
const TRAY_W = TRAY_ART_W;

const browser = await chromium.launch();
for (const [name, size] of [["icon.png", 512], ["128x128.png", 128], ["32x32.png", 32]]) {
  const p = await browser.newPage({ viewport: { width: size, height: size }, deviceScaleFactor: 1 });
  await p.setContent(appPage(size, Math.round(size * 0.2237), Math.round(size * 0.52)));
  await p.locator(".icon").screenshot({ path: new URL(`../${name}`, import.meta.url).pathname, omitBackground: true });
  await p.close();
}
{
  const p = await browser.newPage({ viewport: { width: TRAY_W, height: TRAY_H }, deviceScaleFactor: 1 });
  await p.setContent(trayPage(TRAY_W, TRAY_H, TRAY_ART_W, TRAY_ART_H));
  await p.locator(".tray").screenshot({ path: new URL("../tray-icon.png", import.meta.url).pathname, omitBackground: true });
  await p.close();
}
await browser.close();
console.log(`rendered (tray ${TRAY_W}x${TRAY_H}, artwork ${TRAY_ART_W}x${TRAY_ART_H})`);
