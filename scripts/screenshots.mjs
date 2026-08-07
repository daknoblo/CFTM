// Screenshots the demo for the README. Run against the server, not the static
// export: node scripts/screenshots.mjs <baseURL> <outDir>
import { chromium } from "playwright";
import { mkdir } from "node:fs/promises";

const baseURL = process.argv[2] ?? "http://127.0.0.1:8099";
const outDir = process.argv[3] ?? "docs/screenshots";

// Paths carry no trailing slash: the server routes are exact matches, and a
// silently captured 404 is worse than no screenshot at all.
const shots = [
  { name: "dashboard", path: "/" },
  { name: "tunnel", follow: "a[href^='/tunnels/']" },
  { name: "ingress", path: "/ingress" },
  { name: "audit", path: "/audit" },
  { name: "events", path: "/events" },
];

await mkdir(outDir, { recursive: true });

const browser = await chromium.launch();
const page = await browser.newPage({
  viewport: { width: 1440, height: 900 },
  deviceScaleFactor: 2,
  colorScheme: "dark",
});

for (const shot of shots) {
  let response = null;
  if (shot.path) {
    response = await page.goto(new URL(shot.path, baseURL).href, { waitUntil: "networkidle" });
  } else {
    await Promise.all([
      page.waitForNavigation({ waitUntil: "networkidle" }),
      page.locator(shot.follow).first().click(),
    ]);
  }

  if (response && !response.ok()) {
    throw new Error(`${shot.name}: ${response.url()} returned ${response.status()}`);
  }
  const heading = (await page.locator("h1").first().textContent())?.trim();
  if (!heading) {
    throw new Error(`${shot.name}: ${page.url()} rendered no heading, refusing to ship it`);
  }

  // app.js localizes the timestamps after load.
  await page.waitForTimeout(300);
  await page.screenshot({ path: `${outDir}/${shot.name}.png`, fullPage: true });
  console.log(`captured ${shot.name} (${heading})`);
}

await browser.close();
