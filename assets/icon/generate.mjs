/**
 * Builds the Sumi app icon SVGs, macOS menu bar template icon, desktop PNG/ICNS
 * assets, and Web favicon.
 *
 * Built in the machined architectural slab family of kip, hark, gaze, zoet, quiet, lull, and beck:
 *   - Plate: 824x824 continuous superellipse (n=5) on a 1024 canvas (100 px safe margin),
 *     radiant Sumi flame-orange gradient (#FF8152 -> #F0602E -> #D4421A) with crisp inner rim
 *   - Mark: Two interlocking dialogue slabs canted -8 deg across an exact concentric arc seam —
 *     an organic, full-radius rounded speech bubble (Human, top-left) cradled by a structured
 *     architectural rounded-square speech bubble (Agent, bottom-right). Balances quiet geometric
 *     restraint (2 solid machined slabs, zero literal faces) with unmistakable Human + Agent
 *     conversation and collaboration.
 *   - Optical 3-tier sizing:
 *       > 32 px: machined warm-cream pearl (#FFFFFF -> #FFFBF4 / #F5DFD6) + warm lift shadow
 *     24-32 px: crisp flat #FFFBF4 / #FFEBE2 on solid #F0602E with widened separation channel
 *        16 px: bold high-contrast flat #FFFFFF / #FFFBF4 on solid #F0602E with generous channel
 *   - Menu bar template: crisp 36x36 (@2x 18pt) monochrome black silhouette
 *
 * Usage:
 *   node assets/icon/generate.mjs             # generate SVGs + render all platform assets
 *   node assets/icon/generate.mjs --svg-only  # regenerate source SVGs and favicon only
 */
import { spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// ---- Tunables -------------------------------------------------------------
const C = 512; // canvas center
const PLATE_A = 412; // superellipse half-size => 100 px safe margin
const PLATE_N = 5;
const PLATE_TOP = "#FF8152";
const PLATE_MID = "#F0602E"; // original Sumi flame orange
const PLATE_BOT = "#D4421A";
const PLATE_FLAT = "#F0602E";
const SHADOW_COLOR = "#6E1D07";
const CREAM = "#FFFBF4"; // original Sumi warm cream
const TILT_DEG = -8; // family counter-clockwise cant (hark, gaze, zoet, quiet)

const LARGE_SPEC = {
  gap: 40,
  fillet: 22,
  tilt: TILT_DEG,
};

const FLAT_SPEC = {
  gap: 50,
  fillet: 20,
  tilt: TILT_DEG,
};

const SMALL_SPEC = {
  gap: 64,
  fillet: 18,
  tilt: TILT_DEG,
};

const TRAY_SPEC = {
  gap: 56,
  fillet: 20,
  tilt: TILT_DEG,
};
// ---------------------------------------------------------------------------

const iconDir = dirname(fileURLToPath(import.meta.url));
const repoRoot = join(iconDir, "..", "..");
const desktopDir = join(repoRoot, "desktop");
const desktopAssetsDir = join(desktopDir, "assets");
const webPublicDir = join(repoRoot, "web", "public");
const svgOnly = process.argv.includes("--svg-only");

const fmt = (n) => n.toFixed(2);
const pt = ([x, y]) => `${fmt(x)} ${fmt(y)}`;

/** macOS body: 824x824 superellipse (n=5) centered on the canvas, 100 px safe margin. */
function squircle(steps = 720, digits = 2) {
  const pts = [];
  for (let i = 0; i < steps; i++) {
    const t = (i / steps) * 2 * Math.PI;
    const ct = Math.cos(t);
    const st = Math.sin(t);
    const x = C + PLATE_A * Math.sign(ct) * Math.abs(ct) ** (2 / PLATE_N);
    const y = C + PLATE_A * Math.sign(st) * Math.abs(st) ** (2 / PLATE_N);
    pts.push(`${x.toFixed(digits)} ${y.toFixed(digits)}`);
  }
  return `M${pts.join("L")}Z`;
}

/**
 * Builds the two interlocking Human (organic rounded) + Agent (structured square)
 * dialogue slabs in local coordinates centered at (0, 0), with an unbroken concentric
 * arc channel and mathematically exact tangent circular corner fillets.
 */
function buildDialogue({ gap, fillet, tilt }) {
  // Human dialogue slab (top-left): full-radius organic pill-dome with crisp bottom-left speech corner
  const hx0 = -260;
  const hy0 = -234;
  const hx1 = 72;
  const hy1 = 8;
  const hr = (hy1 - hy0) / 2; // 121 px semicircle radius
  const hrBL = 22;

  const human = [
    `M ${fmt(hx0 + hr)} ${fmt(hy0)}`,
    `L ${fmt(hx1 - hr)} ${fmt(hy0)}`,
    `A ${fmt(hr)} ${fmt(hr)} 0 0 1 ${fmt(hx1)} ${fmt(hy0 + hr)}`,
    `A ${fmt(hr)} ${fmt(hr)} 0 0 1 ${fmt(hx1 - hr)} ${fmt(hy1)}`,
    `L ${fmt(hx0 + hrBL)} ${fmt(hy1)}`,
    `A ${fmt(hrBL)} ${fmt(hrBL)} 0 0 1 ${fmt(hx0)} ${fmt(hy1 - hrBL)}`,
    `L ${fmt(hx0)} ${fmt(hy0 + hr)}`,
    `A ${fmt(hr)} ${fmt(hr)} 0 0 1 ${fmt(hx0 + hr)} ${fmt(hy0)}`,
    "Z",
  ].join(" ");

  // Agent dialogue slab (bottom-right): structured rounded square with crisp bottom-right speech corner,
  // carved cleanly along the concentric arc centered at Human's right semicircle center (cxB, cyB).
  const ax0 = -44; // > cxB (-49) so the concentric arc intersects top and left edges directly without a shelf
  const ay0 = -20;
  const ax1 = 260;
  const ay1 = 234;
  const arTR = 46;
  const arBR = 20;
  const arBL = 46;

  const cxB = hx1 - hr; // -49
  const cyB = hy0 + hr; // -113
  const Rcut = hr + gap;
  const r = fillet;

  // Exact tangent circular fillet between top straight edge (y = ay0) and concentric cutout arc (cxB, cyB, Rcut)
  const fyT = ay0 + r;
  const fxT = cxB + Math.sqrt(Math.max(0, (Rcut + r) ** 2 - (fyT - cyB) ** 2));
  const sT = Rcut / (Rcut + r);
  const pArcTop = [cxB + (fxT - cxB) * sT, cyB + (fyT - cyB) * sT];
  const pEdgeTop = [fxT, ay0];

  // Exact tangent circular fillet between left straight edge (x = ax0) and concentric cutout arc (cxB, cyB, Rcut)
  const fxL = ax0 + r;
  const fyL = cyB + Math.sqrt(Math.max(0, (Rcut + r) ** 2 - (fxL - cxB) ** 2));
  const sL = Rcut / (Rcut + r);
  const pArcLeft = [cxB + (fxL - cxB) * sL, cyB + (fyL - cyB) * sL];
  const pEdgeLeft = [ax0, fyL];

  const agent = [
    `M ${pt(pArcTop)}`,
    `A ${fmt(r)} ${fmt(r)} 0 0 1 ${pt(pEdgeTop)}`,
    `L ${fmt(ax1 - arTR)} ${fmt(ay0)}`,
    `A ${fmt(arTR)} ${fmt(arTR)} 0 0 1 ${fmt(ax1)} ${fmt(ay0 + arTR)}`,
    `L ${fmt(ax1)} ${fmt(ay1 - arBR)}`,
    `A ${fmt(arBR)} ${fmt(arBR)} 0 0 1 ${fmt(ax1 - arBR)} ${fmt(ay1)}`,
    `L ${fmt(ax0 + arBL)} ${fmt(ay1)}`,
    `A ${fmt(arBL)} ${fmt(arBL)} 0 0 1 ${fmt(ax0)} ${fmt(ay1 - arBL)}`,
    `L ${pt(pEdgeLeft)}`,
    `A ${fmt(r)} ${fmt(r)} 0 0 1 ${pt(pArcLeft)}`,
    `A ${fmt(Rcut)} ${fmt(Rcut)} 0 0 0 ${pt(pArcTop)}`,
    "Z",
  ].join(" ");

  return { tilt, human, agent };
}

function renderGroup(m, fillH, fillA, extraTransform = "") {
  const tx = `translate(512 512) ${extraTransform}rotate(${m.tilt})`.trim();
  return `<g transform="${tx}">
    <path d="${m.agent}" fill="${fillA}"/>
    <path d="${m.human}" fill="${fillH}"/>
  </g>`;
}

const SQ = squircle(720, 2);
const large = buildDialogue(LARGE_SPEC);
const flat = buildDialogue(FLAT_SPEC);
const small = buildDialogue(SMALL_SPEC);
const tray = buildDialogue(TRAY_SPEC);

const metalSvg = `<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024" viewBox="0 0 1024 1024">
  <!-- Generated by assets/icon/generate.mjs; edit the script, not this file. -->
  <defs>
    <radialGradient id="plate" cx="0.28" cy="0.14" r="0.96">
      <stop offset="0%" stop-color="${PLATE_TOP}"/>
      <stop offset="48%" stop-color="${PLATE_MID}"/>
      <stop offset="100%" stop-color="${PLATE_BOT}"/>
    </radialGradient>
    <linearGradient id="slab-human" x1="0.15" y1="0" x2="0.85" y2="1">
      <stop offset="0" stop-color="#FFFFFF"/>
      <stop offset="1" stop-color="${CREAM}"/>
    </linearGradient>
    <linearGradient id="slab-agent" x1="0.15" y1="0" x2="0.85" y2="1">
      <stop offset="0" stop-color="#FFFFFF"/>
      <stop offset="1" stop-color="#F5DFD6"/>
    </linearGradient>
    <filter id="lift" x="-25%" y="-25%" width="150%" height="160%">
      <feDropShadow dx="0" dy="18" stdDeviation="22" flood-color="${SHADOW_COLOR}" flood-opacity="0.38"/>
    </filter>
    <clipPath id="plate-clip"><path d="${SQ}"/></clipPath>
  </defs>
  <path d="${SQ}" fill="url(#plate)"/>
  <path d="${SQ}" fill="none" stroke="#FFFFFF" stroke-opacity="0.28" stroke-width="3.5" clip-path="url(#plate-clip)"/>
  <g filter="url(#lift)">
    ${renderGroup(large, "url(#slab-human)", "url(#slab-agent)")}
  </g>
</svg>
`;

const flatSvg = `<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024" viewBox="0 0 1024 1024">
  <!-- Generated by assets/icon/generate.mjs; edit the script, not this file. -->
  <path d="${SQ}" fill="${PLATE_FLAT}"/>
  ${renderGroup(flat, CREAM, "#FFEBE2")}
</svg>
`;

const smallSvg = `<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024" viewBox="0 0 1024 1024">
  <!-- Generated by assets/icon/generate.mjs; edit the script, not this file. -->
  <path d="${SQ}" fill="${PLATE_FLAT}"/>
  ${renderGroup(small, "#FFFFFF", CREAM)}
</svg>
`;

const traySvg = `<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1024 1024" width="36" height="36">
  <!-- Generated by assets/icon/generate.mjs; edit the script, not this file. -->
  ${renderGroup(tray, "#000000", "#000000", "scale(1.42) ")}
</svg>
`;

mkdirSync(iconDir, { recursive: true });
mkdirSync(webPublicDir, { recursive: true });

const metalSvgPath = join(iconDir, "appicon.svg");
const flatSvgPath = join(iconDir, "appicon-flat.svg");
const smallSvgPath = join(iconDir, "appicon-small.svg");
const traySvgPath = join(iconDir, "tray-template.svg");
const faviconPath = join(webPublicDir, "favicon.svg");

writeFileSync(metalSvgPath, metalSvg);
writeFileSync(flatSvgPath, flatSvg);
writeFileSync(smallSvgPath, smallSvg);
writeFileSync(traySvgPath, traySvg);
writeFileSync(faviconPath, flatSvg);

console.log(
  "Generated SVGs: assets/icon/appicon.svg, appicon-flat.svg, appicon-small.svg, tray-template.svg, web/public/favicon.svg",
);

if (svgOnly) {
  process.exit(0);
}

// ---- Platform icon rasterization ------------------------------------------
function svgForSize(size) {
  if (size <= 16) return smallSvgPath;
  if (size <= 32) return flatSvgPath;
  return metalSvgPath;
}

function renderSvgToPng(svgPath, size) {
  const res = spawnSync("rsvg-convert", ["-w", String(size), "-h", String(size), svgPath], {
    maxBuffer: 32 * 1024 * 1024,
  });
  if (res.error) {
    throw new Error(
      `Failed to run rsvg-convert (${res.error.message}). Install librsvg (e.g. \`brew install librsvg\`).`,
    );
  }
  if (res.status !== 0) {
    throw new Error(`rsvg-convert failed for ${svgPath} @ ${size}px: ${res.stderr.toString("utf8")}`);
  }
  return res.stdout;
}

mkdirSync(desktopAssetsDir, { recursive: true });

writeFileSync(join(desktopAssetsDir, "appicon.png"), renderSvgToPng(metalSvgPath, 1024));
writeFileSync(join(desktopAssetsDir, "tray_template.png"), renderSvgToPng(traySvgPath, 36));

const tmpRoot = mkdtempSync(join(tmpdir(), "sumi-icon-"));
const iconsetDir = join(tmpRoot, "AppIcon.iconset");
mkdirSync(iconsetDir, { recursive: true });
try {
  const icnsSlots = [
    ["icon_16x16.png", 16],
    ["icon_16x16@2x.png", 32],
    ["icon_32x32.png", 32],
    ["icon_32x32@2x.png", 64],
    ["icon_128x128.png", 128],
    ["icon_128x128@2x.png", 256],
    ["icon_256x256.png", 256],
    ["icon_256x256@2x.png", 512],
    ["icon_512x512.png", 512],
    ["icon_512x512@2x.png", 1024],
  ];
  for (const [name, size] of icnsSlots) {
    writeFileSync(join(iconsetDir, name), renderSvgToPng(svgForSize(size), size));
  }
  const icnsOut = join(desktopDir, "AppIcon.icns");
  const iconutil = spawnSync("iconutil", ["-c", "icns", iconsetDir, "-o", icnsOut]);
  if (iconutil.error || iconutil.status !== 0) {
    throw new Error(
      `Failed to generate AppIcon.icns via iconutil (${iconutil.error?.message ?? iconutil.stderr.toString("utf8")}).`,
    );
  }
} finally {
  rmSync(tmpRoot, { recursive: true, force: true });
}

console.log("Rendered desktop assets: desktop/AppIcon.icns, desktop/assets/appicon.png, desktop/assets/tray_template.png");
