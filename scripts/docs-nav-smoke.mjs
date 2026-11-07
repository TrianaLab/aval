#!/usr/bin/env node
// Opens the mobile drawer (hamburger menu) of the built docs site at a phone
// width and checks that the primary nav panel is on screen, on top of the
// overlay and full of links, on the home page and on an inner page, in the
// light and the dark scheme. No dependencies: Chrome headless driven over the
// DevTools protocol on a pipe (--remote-debugging-pipe).
//
// Usage: node scripts/docs-nav-smoke.mjs <site-dir> [screenshot-dir]
// It serves <site-dir> (the `make docs-site` output) on a free localhost port.
// CHROME overrides the browser binary.
import { spawn } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, statSync, writeFileSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { extname, join, normalize } from "node:path";

const [site, shots] = process.argv.slice(2);
if (!site) {
  console.error("usage: docs-nav-smoke.mjs <site-dir> [screenshot-dir]");
  process.exit(2);
}
const TYPES = { ".html": "text/html", ".css": "text/css", ".js": "text/javascript", ".svg": "image/svg+xml", ".json": "application/json", ".woff2": "font/woff2" };
const server = createServer((req, res) => {
  let file = join(site, normalize(decodeURIComponent(new URL(req.url, "http://x").pathname)));
  if (existsSync(file) && statSync(file).isDirectory()) file = join(file, "index.html");
  if (!existsSync(file)) return res.writeHead(404).end();
  res.writeHead(200, { "content-type": TYPES[extname(file)] || "application/octet-stream" }).end(readFileSync(file));
});
await new Promise((r) => server.listen(0, "127.0.0.1", r));
const base = `http://127.0.0.1:${server.address().port}/`;
const PAGES = ["", "install/"];
const SCHEMES = ["light", "dark"];
const WIDTH = 390;
const HEIGHT = 844;

const chrome =
  process.env.CHROME ||
  [
    "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
    "/usr/bin/google-chrome",
    "/usr/bin/chromium",
    "/usr/bin/chromium-browser",
  ].find(existsSync);
if (!chrome) {
  console.error("no Chrome found; set CHROME");
  process.exit(2);
}

const profile = mkdtempSync(join(tmpdir(), "aval-nav-smoke-"));
const proc = spawn(
  chrome,
  ["--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run", "--remote-debugging-pipe", `--user-data-dir=${profile}`, "about:blank"],
  { stdio: ["ignore", "ignore", "ignore", "pipe", "pipe"] },
);

let nextId = 0;
const pending = new Map();
const listeners = [];
let buf = "";
proc.stdio[4].on("data", (chunk) => {
  buf += chunk;
  let i;
  while ((i = buf.indexOf("\0")) >= 0) {
    const msg = JSON.parse(buf.slice(0, i));
    buf = buf.slice(i + 1);
    if (msg.id !== undefined && pending.has(msg.id)) {
      const { resolve, reject } = pending.get(msg.id);
      pending.delete(msg.id);
      msg.error ? reject(new Error(JSON.stringify(msg.error))) : resolve(msg.result);
    } else {
      for (const l of listeners) l(msg);
    }
  }
});
function send(method, params = {}, sessionId) {
  const id = ++nextId;
  proc.stdio[3].write(JSON.stringify({ id, method, params, sessionId }) + "\0");
  return new Promise((resolve, reject) => pending.set(id, { resolve, reject }));
}
function once(method, sessionId) {
  return new Promise((resolve) => {
    const l = (msg) => {
      if (msg.method === method && msg.sessionId === sessionId) {
        listeners.splice(listeners.indexOf(l), 1);
        resolve(msg.params);
      }
    };
    listeners.push(l);
  });
}
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

// Returns a list of problems with the open drawer, empty when it is fine.
const probe = `(() => {
  const p = [];
  const nav = document.querySelector(".md-sidebar--primary");
  if (!nav) return ["no .md-sidebar--primary on the page"];
  const r = nav.getBoundingClientRect();
  if (r.width < 200 || r.height < 200) p.push("drawer size " + r.width + "x" + r.height);
  if (r.left < -1 || r.right > innerWidth + 1 || r.top < -1) p.push("drawer off screen: " + JSON.stringify(r));
  const links = [...nav.querySelectorAll("a.md-nav__link")];
  if (links.length < 10) p.push(links.length + " links in the drawer");
  const hrefs = links.map((a) => new URL(a.href).pathname);
  for (const want of ["/install/", "/concepts/trust/", "/cli-reference/"])
    if (!hrefs.some((h) => h.endsWith(want))) p.push("drawer has no link to " + want);
  const hit = document.elementFromPoint(r.left + r.width / 2, Math.min(r.top + r.height / 2, innerHeight - 1));
  if (!nav.contains(hit)) p.push("drawer is covered by " + (hit ? hit.className || hit.tagName : "nothing"));
  return p;
})()`;

let failed = 0;
try {
  await send("Browser.getVersion");
  for (const page of PAGES) {
    for (const scheme of SCHEMES) {
      const { targetId } = await send("Target.createTarget", { url: "about:blank" });
      const { sessionId } = await send("Target.attachToTarget", { targetId, flatten: true });
      await send("Page.enable", {}, sessionId);
      await send("Emulation.setDeviceMetricsOverride", { width: WIDTH, height: HEIGHT, deviceScaleFactor: 2, mobile: true }, sessionId);
      await send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: scheme }, { name: "prefers-reduced-motion", value: "reduce" }] }, sessionId);
      const loaded = once("Page.loadEventFired", sessionId);
      await send("Page.navigate", { url: base + page }, sessionId);
      await loaded;
      await sleep(300);
      // Tap the hamburger like a user: a real pointer event at its centre.
      const eval_ = async (expression) =>
        (await send("Runtime.evaluate", { expression, returnByValue: true }, sessionId)).result.value;
      const btn = await eval_(`(() => { const b = document.querySelector('label.md-header__button[for="__drawer"]'); if (!b) return null; const r = b.getBoundingClientRect(); return r.width ? { x: r.left + r.width / 2, y: r.top + r.height / 2 } : null; })()`);
      const name = `${page.replace(/\/$/, "") || "home"} ${scheme}`;
      let problems;
      if (!btn) {
        problems = ["no visible drawer button"];
      } else {
        for (const type of ["mousePressed", "mouseReleased"])
          await send("Input.dispatchMouseEvent", { type, x: btn.x, y: btn.y, button: "left", clickCount: 1 }, sessionId);
        await sleep(600);
        problems = await eval_(probe);
      }
      if (shots) {
        mkdirSync(shots, { recursive: true });
        const { data } = await send("Page.captureScreenshot", { format: "png" }, sessionId);
        writeFileSync(join(shots, `${name.replace(/[^a-z0-9]+/g, "-")}.png`), Buffer.from(data, "base64"));
      }
      if (problems.length) {
        failed++;
        console.log(`FAIL ${name}: ${problems.join("; ")}`);
      } else {
        console.log(`ok   ${name}`);
      }
      await send("Target.closeTarget", { targetId });
    }
  }
} finally {
  proc.kill();
  server.close();
  await new Promise((r) => (proc.exitCode === null ? proc.once("exit", r) : r()));
  rmSync(profile, { recursive: true, force: true, maxRetries: 5 });
}
process.exit(failed ? 1 : 0);
