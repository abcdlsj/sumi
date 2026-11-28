/**
 * Builds the Sumi app icon SVGs, macOS menu bar template icon, desktop PNG/ICNS
 * assets, and Web favicon.
 *
 * Built in the machined architectural slab family of kip, hark, gaze, zoet, quiet, lull, and beck:
 *   - Plate: 824x824 continuous superellipse (n=5) on a 1024 canvas (100 px safe margin),
 *     radiant Sumi flame-orange gradient (#FF8152 -> #F0602E -> #D4421A) with crisp inner rim
 *   - Mark: Two equal-area machined slabs interlocked along a -8 deg diagonal —
 *     a pure luminous Circle (Human) nesting into a precision-carved concentric cradle
 *     of a Rounded Square (Agent), representing equal peers collaborating in one shared Space.
 *   - Optical 3-tier sizing:
 *       > 32 px: machined warm-cream pearl (#FFFFFF -> #FFFBF4 / #F5DFD6) + warm lift shadow
 *     24-32 px: crisp flat #FFFBF4 / #FFEBE2 on solid #F0602E with widened negative-space channel
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

// Symmetric 512x512 diagonal embrace with ~1:1 equal visual area between Human (circle)
// and Agent (carved rounded square), and identical thickness on both cradling arms.
const LARGE_SPEC = {
  hr: 166,
  S: 184,
  rSq: 46,
  sepX: 162,
  sepY: 162,
  gap: 44,
  fillet: 24,
  angleDeg: TILT_DEG,
};

const FLAT_SPEC = {
  hr: 168,
  S: 186,
  rSq: 44,
  sepX: 166,
  sepY: 166,
  gap: 54,
  fillet: 22,
  angleDeg: TILT_DEG,
};

const SMALL_SPEC = {
  hr: 172,
  S: 192,
  rSq: 42,
  sepX: 172,
  sepY: 172,
  gap: 68,
  fillet: 20,
  angleDeg: TILT_DEG,
};

const TRAY_SPEC = {
  hr: 232,
  S: 256,
  rSq: 62,
  sepX: 230,
  sepY: 230,
  gap: 82,
  fillet: 30,
  angleDeg: TILT_DEG,
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
 * Constructs the two-slab Human (Circle) + Agent (Carved Rounded Square) emblem
 * with exact analytical tangent circular corner fillets and automatic bbox centering.
 */
function buildEmbrace({ hr, S, rSq, sepX, sepY, gap, fillet, angleDeg }) {
  let hx = -sepX / 2;
  let hy = -sepY / 2;
  let ax = sepX / 2;
  let ay = sepY / 2;

  // Center the unrotated composite bounding box exactly at origin before tilting
  const minX = Math.min(hx - hr, ax - S);
  const maxX = Math.max(hx + hr, ax + S);
  const minY = Math.min(hy - hr, ay - S);
  const maxY = Math.max(hy + hr, ay + S);
  const shiftX = -(minX + maxX) / 2;
  const shiftY = -(minY + maxY) / 2;
  hx += shiftX;
  hy += shiftY;
  ax += shiftX;
  ay += shiftY;

  const rad = (angleDeg * Math.PI) / 180;
  const cos = Math.cos(rad);
  const sin = Math.sin(rad);
  const xf = (x, y) => [C + x * cos - y * sin, C + x * sin + y * cos];

  const Rcut = hr + gap;
  const xL = ax - S;
  const xR = ax + S;
  const yT = ay - S;
  const yB = ay + S;
  const r = fillet;

  // Exact tangent circular fillet between top straight edge (y = yT) and cutout circle (hx, hy, Rcut)
  const fyTop = yT + r;
  const fxTop = hx + Math.sqrt(Math.max(0, (Rcut + r) ** 2 - (fyTop - hy) ** 2));
  const sTop = Rcut / (Rcut + r);
  const pTopArc = xf(hx + (fxTop - hx) * sTop, hy + (fyTop - hy) * sTop);
  const pTopEdge = xf(fxTop, yT);

  // Exact tangent circular fillet between left straight edge (x = xL) and cutout circle (hx, hy, Rcut)
  const fxLeft = xL + r;
  const fyLeft = hy + Math.sqrt(Math.max(0, (Rcut + r) ** 2 - (fxLeft - hx) ** 2));
  const sLeft = Rcut / (Rcut + r);
  const pLeftArc = xf(hx + (fxLeft - hx) * sLeft, hy + (fyLeft - hy) * sLeft);
  const pLeftEdge = xf(xL, fyLeft);

  const pTR1 = xf(xR - rSq, yT);
  const pTR2 = xf(xR, yT + rSq);
  const pBR1 = xf(xR, yB - rSq);
  const pBR2 = xf(xR - rSq, yB);
  const pBL1 = xf(xL + rSq, yB);
  const pBL2 = xf(xL, yB - rSq);

  const agentSlab = [
    `M ${pt(pTopArc)}`,
    `A ${fmt(r)} ${fmt(r)} 0 0 1 ${pt(pTopEdge)}`,
    `L ${pt(pTR1)}`,
    `A ${fmt(rSq)} ${fmt(rSq)} 0 0 1 ${pt(pTR2)}`,
    `L ${pt(pBR1)}`,
    `A ${fmt(rSq)} ${fmt(rSq)} 0 0 1 ${pt(pBR2)}`,
    `L ${pt(pBL1)}`,
    `A ${fmt(rSq)} ${fmt(rSq)} 0 0 1 ${pt(pBL2)}`,
    `L ${pt(pLeftEdge)}`,
    `A ${fmt(r)} ${fmt(r)} 0 0 1 ${pt(pLeftArc)}`,
    `A ${fmt(Rcut)} ${fmt(Rcut)} 0 0 0 ${pt(pTopArc)}`,
    "Z",
  ].join(" ");

  const [hcx, hcy] = xf(hx, hy);
  return {
    human: { cx: hcx, cy: hcy, r: hr },
    agent: agentSlab,
  };
}

const SQ = squircle(720, 2);
const large = buildEmbrace(LARGE_SPEC);
const flat = buildEmbrace(FLAT_SPEC);
const small = buildEmbrace(SMALL_SPEC);
const tray = buildEmbrace(TRAY_SPEC);

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
    <path d="${large.agent}" fill="url(#slab-agent)"/>
    <circle cx="${fmt(large.human.cx)}" cy="${fmt(large.human.cy)}" r="${fmt(large.human.r)}" fill="url(#slab-human)"/>
  </g>
</svg>
`;

const flatSvg = `<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024" viewBox="0 0 1024 1024">
  <!-- Generated by assets/icon/generate.mjs; edit the script, not this file. -->
  <path d="${SQ}" fill="${PLATE_FLAT}"/>
  <path d="${flat.agent}" fill="#FFEBE2"/>
  <circle cx="${fmt(flat.human.cx)}" cy="${fmt(flat.human.cy)}" r="${fmt(flat.human.r)}" fill="${CREAM}"/>
</svg>
`;

const smallSvg = `<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024" viewBox="0 0 1024 1024">
  <!-- Generated by assets/icon/generate.mjs; edit the script, not this file. -->
  <path d="${SQ}" fill="${PLATE_FLAT}"/>
  <path d="${small.agent}" fill="${CREAM}"/>
  <circle cx="${fmt(small.human.cx)}" cy="${fmt(small.human.cy)}" r="${fmt(small.human.r)}" fill="#FFFFFF"/>
</svg>
`;

const traySvg = `<?xml version="1.0" encoding="UTF-8"?>
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 1024 1024" width="36" height="36">
  <!-- Generated by assets/icon/generate.mjs; edit the script, not this file. -->
  <path d="${tray.agent}" fill="#000000"/>
  <circle cx="${fmt(tray.human.cx)}" cy="${fmt(tray.human.cy)}" r="${fmt(tray.human.r)}" fill="#000000"/>
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
