// Screenshots the demo site so the README shows the real UI rather than a
// hand-drawn impression. Run with: node scripts/screenshots.mjs <baseURL> <outDir>
import { chromium } from "playwright";
import { mkdir } from "node:fs/promises";

const baseURL = process.argv[2] ?? "http://127.0.0.1:8099";
const outDir = process.argv[3] ?? "docs/screenshots";

const shots = [
  { name: "dashboard", path: "/" },
  { name: "tunnel", path: null, follow: "a[href*='/tunnels/']" },
  { name: "ingress", path: "/ingress/" },
  { name: "audit", path: "/audit/" },
  { name: "events", path: "/events/" },
];

await mkdir(outDir, { recursive: true });

const browser = await chromium.launch();
const page = await browser.newPage({
  viewport: { width: 1440, height: 900 },
  deviceScaleFactor: 2,
  colorScheme: "dark",
});

for (const shot of shots) {
  if (shot.path) {
    await page.goto(new URL(shot.path, baseURL).href, { waitUntil: "networkidle" });
  } else {
    await page.locator(shot.follow).first().click();
    await page.waitForLoadState("networkidle");
  }
  // The timestamps are localized by app.js after load.
  await page.waitForTimeout(300);
  await page.screenshot({ path: `${outDir}/${shot.name}.png`, fullPage: true });
  console.log(`captured ${shot.name}`);
}

await browser.close();
