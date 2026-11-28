/**
 * Builds the Sumi app icon SVGs, macOS menu bar template icon, desktop PNG/ICNS
 * assets, and Web favicon.
 *
 * Built in the machined architectural slab family of kip, hark, gaze, zoet, quiet, lull, and beck:
 *   - Plate: 824x824 continuous superellipse (n=5) on a 1024 canvas (100 px safe margin),
 *     radiant Sumi flame-orange gradient (#FF8152 -> #F0602E -> #D4421A) with crisp inner rim
 *   - Mark: Human and Agent as equal teammates standing shoulder-to-shoulder along a -5 deg cant —
 *     a warm organic Human silhouette (circle head + arched shoulder dome) interlocked via an
 *     exact concentric arc channel with a structured Sumi Agent silhouette (rounded-square head
 *     with two pill eyes glancing gently toward the Human + precision-carved architectural shoulder).
 *   - Optical 3-tier sizing:
 *       > 32 px: machined warm-cream pearl (#FFFFFF -> #FFFBF4 / #F5DFD6) + warm lift shadow
 *     24-32 px: crisp flat #FFFBF4 / #FFEBE2 on solid #F0602E with widened seam & bolder eyes
 *        16 px: bold high-contrast flat silhouette (eyes omitted for pixel-grid clarity)
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
const TILT_DEG = -5;

const LARGE_SPEC = {
  gap: 34,
  neckGap: 42,
  eyeW: 28,
  eyeH: 50,
  eyeGap: 38,
  eyeShiftX: -9,
  showEyes: true,
  tilt: TILT_DEG,
};

const FLAT_SPEC = {
  gap: 42,
  neckGap: 48,
  eyeW: 32,
  eyeH: 54,
  eyeGap: 36,
  eyeShiftX: -8,
  showEyes: true,
  tilt: TILT_DEG,
};

const SMALL_SPEC = {
  gap: 50,
  neckGap: 56,
  eyeW: 0,
  eyeH: 0,
  eyeGap: 0,
  eyeShiftX: 0,
  showEyes: false,
  tilt: TILT_DEG,
};

const TRAY_SPEC = {
  gap: 46,
  neckGap: 50,
  eyeW: 34,
  eyeH: 56,
  eyeGap: 36,
  eyeShiftX: -8,
  showEyes: true,
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

/** Rounded rectangle subpath; `ccw = true` winds counter-clockwise for even-odd cutouts. */
function rrect(x, y, w, h, r, ccw = false) {
  if (!ccw) {
    return [
      `M ${fmt(x + r)} ${fmt(y)}`,
      `L ${fmt(x + w - r)} ${fmt(y)}`,
      `A ${fmt(r)} ${fmt(r)} 0 0 1 ${fmt(x + w)} ${fmt(y + r)}`,
      `L ${fmt(x + w)} ${fmt(y + h - r)}`,
      `A ${fmt(r)} ${fmt(r)} 0 0 1 ${fmt(x + w - r)} ${fmt(y + h)}`,
      `L ${fmt(x + r)} ${fmt(y + h)}`,
      `A ${fmt(r)} ${fmt(r)} 0 0 1 ${fmt(x)} ${fmt(y + h - r)}`,
      `L ${fmt(x)} ${fmt(y + r)}`,
      `A ${fmt(r)} ${fmt(r)} 0 0 1 ${fmt(x + r)} ${fmt(y)}`,
      "Z",
    ].join(" ");
  }
  return [
    `M ${fmt(x + r)} ${fmt(y)}`,
    `A ${fmt(r)} ${fmt(r)} 0 0 0 ${fmt(x)} ${fmt(y + r)}`,
    `L ${fmt(x)} ${fmt(y + h - r)}`,
    `A ${fmt(r)} ${fmt(r)} 0 0 0 ${fmt(x + r)} ${fmt(y + h)}`,
    `L ${fmt(x + w - r)} ${fmt(y + h)}`,
    `A ${fmt(r)} ${fmt(r)} 0 0 0 ${fmt(x + w)} ${fmt(y + h - r)}`,
    `L ${fmt(x + w)} ${fmt(y + r)}`,
    `A ${fmt(r)} ${fmt(r)} 0 0 0 ${fmt(x + w - r)} ${fmt(y)}`,
    "Z",
  ].join(" ");
}

/**
 * Builds the shoulder-to-shoulder Human + Sumi Agent emblem in local coords
 * centered at (0, 0), with exact analytical circular corner fillets on the
 * Agent shoulder's concentric cradle cutout.
 */
function buildTeammates({ gap, neckGap, eyeW, eyeH, eyeGap, eyeShiftX, showEyes, tilt }) {
  // Equal-height heads centered at y = headY
  const hx = -124;
  const ax = 132;
  const hR = 96;
  const aHeadW = 200;
  const aHeadH = 186;
  const aHeadR = 46;

  // Vertical layout: total height ~456 px centered at y = 0
  const headY = -128;
  const yBot = 224;
  const Rsh = 132;
  const hBodyTop = headY + hR + neckGap;
  const domeCy = hBodyTop + Rsh;
  const rBot = 36;

  const hBody = [
    `M ${fmt(hx - Rsh)} ${fmt(domeCy)}`,
    `A ${fmt(Rsh)} ${fmt(Rsh)} 0 0 1 ${fmt(hx + Rsh)} ${fmt(domeCy)}`,
    `L ${fmt(hx + Rsh)} ${fmt(yBot - rBot)}`,
    `A ${fmt(rBot)} ${fmt(rBot)} 0 0 1 ${fmt(hx + Rsh - rBot)} ${fmt(yBot)}`,
    `L ${fmt(hx - Rsh + rBot)} ${fmt(yBot)}`,
    `A ${fmt(rBot)} ${fmt(rBot)} 0 0 1 ${fmt(hx - Rsh)} ${fmt(yBot - rBot)}`,
    "Z",
  ].join(" ");

  const aHeadX = ax - aHeadW / 2;
  const aHeadY = headY - aHeadH / 2;
  const aHeadParts = [rrect(aHeadX, aHeadY, aHeadW, aHeadH, aHeadR, false)];
  if (showEyes) {
    const er = eyeW / 2;
    const eyeCenterX = ax + eyeShiftX;
    const eyeY = headY - eyeH / 2 + 2;
    aHeadParts.push(rrect(eyeCenterX - eyeGap / 2 - eyeW, eyeY, eyeW, eyeH, er, true));
    aHeadParts.push(rrect(eyeCenterX + eyeGap / 2, eyeY, eyeW, eyeH, er, true));
  }
  const aHead = aHeadParts.join(" ");

  // Agent shoulder block aligns its top edge to the exact same neckGap below Agent's head
  const yTopA = headY + aHeadH / 2 + neckGap;
  const xRA = 256;
  const rTopRA = 54;
  const rBotA = 34;
  const rFillet = 20;
  const Rcut = Rsh + gap;
  const xLA = hx + Rsh + gap;

  // Exact tangent circular fillet between top horizontal edge (y = yTopA) and concentric arc (hx, domeCy, Rcut)
  const fyT = yTopA + rFillet;
  const fxT = hx + Math.sqrt(Math.max(0, (Rcut + rFillet) ** 2 - (fyT - domeCy) ** 2));
  const sT = Rcut / (Rcut + rFillet);
  const pArcTop = [hx + (fxT - hx) * sT, domeCy + (fyT - domeCy) * sT];
  const pEdgeTop = [fxT, yTopA];

  const aBody = [
    `M ${pt(pArcTop)}`,
    `A ${fmt(rFillet)} ${fmt(rFillet)} 0 0 1 ${pt(pEdgeTop)}`,
    `L ${fmt(xRA - rTopRA)} ${fmt(yTopA)}`,
    `A ${fmt(rTopRA)} ${fmt(rTopRA)} 0 0 1 ${fmt(xRA)} ${fmt(yTopA + rTopRA)}`,
    `L ${fmt(xRA)} ${fmt(yBot - rBotA)}`,
    `A ${fmt(rBotA)} ${fmt(rBotA)} 0 0 1 ${fmt(xRA - rBotA)} ${fmt(yBot)}`,
    `L ${fmt(xLA + 24)} ${fmt(yBot)}`,
    `A 24 24 0 0 1 ${fmt(xLA)} ${fmt(yBot - 24)}`,
    `L ${fmt(xLA)} ${fmt(domeCy)}`,
    `A ${fmt(Rcut)} ${fmt(Rcut)} 0 0 0 ${pt(pArcTop)}`,
    "Z",
  ].join(" ");

  return {
    tilt,
    humanHead: { cx: hx, cy: headY, r: hR },
    humanBody: hBody,
    agentHead: aHead,
    agentBody: aBody,
  };
}

function renderGroup(m, fillH, fillA, extraTransform = "") {
  const tx = `translate(512 512) ${extraTransform}rotate(${m.tilt})`.trim();
  return `<g transform="${tx}">
    <path d="${m.agentHead}" fill="${fillA}" fill-rule="evenodd"/>
    <path d="${m.agentBody}" fill="${fillA}"/>
    <circle cx="${fmt(m.humanHead.cx)}" cy="${fmt(m.humanHead.cy)}" r="${fmt(m.humanHead.r)}" fill="${fillH}"/>
    <path d="${m.humanBody}" fill="${fillH}"/>
  </g>`;
}

const SQ = squircle(720, 2);
const large = buildTeammates(LARGE_SPEC);
const flat = buildTeammates(FLAT_SPEC);
const small = buildTeammates(SMALL_SPEC);
const tray = buildTeammates(TRAY_SPEC);

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
