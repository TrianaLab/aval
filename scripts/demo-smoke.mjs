// Browser smoke test of the home page's "Try it" terminal (make
// docs-demo-smoke, in ci-docs), against a built site:
//
//   node scripts/demo-smoke.mjs site
//
// It serves the directory on 127.0.0.1, drives headless Chrome over the
// DevTools protocol (a pipe, so no dependency) and checks that nothing is
// fetched before the visitor starts the demo by clicking the terminal body,
// that typing `aval run` twice ends in `verified`, that `tamper` then
// `aval verify` exits 5, and that without WebAssembly (started from the
// keyboard: focus the terminal, press Enter) the transcript stays and the
// page says why.
// CHROME=<path> picks the browser; SCREENSHOTS=<dir> also saves the home
// "see it run" section before the click, in light and dark, at 1440 and 390
// pixels wide, and after the second `aval run`.
import { spawn, execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, rmSync, writeFileSync, existsSync, statSync } from "node:fs";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { extname, join, normalize, resolve, sep } from "node:path";

const root = resolve(process.argv[2] || "site");
const types = { ".html": "text/html", ".js": "text/javascript", ".css": "text/css", ".wasm": "application/wasm", ".svg": "image/svg+xml", ".woff2": "font/woff2", ".png": "image/png", ".json": "application/json" };
const server = createServer((req, res) => {
  let p = normalize(join(root, decodeURIComponent(new URL(req.url, "http://x").pathname)));
  if (p !== root && !p.startsWith(root + sep)) return res.writeHead(403).end();
  if (existsSync(p) && statSync(p).isDirectory()) p = join(p, "index.html");
  if (!existsSync(p)) return res.writeHead(404).end();
  res.writeHead(200, { "content-type": types[extname(p)] || "application/octet-stream" }).end(readFileSync(p));
});
await new Promise((r) => server.listen(0, "127.0.0.1", r));
const site = `http://127.0.0.1:${server.address().port}/`;

function chrome() {
  const names = [process.env.CHROME, "google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"];
  for (const n of names.filter(Boolean)) {
    try {
      execFileSync(n, ["--version"], { stdio: "ignore" });
      return n;
    } catch {}
  }
  throw new Error("no Chrome or Chromium found; set CHROME");
}

const profile = mkdtempSync(join(tmpdir(), "aval-smoke-"));
// CI runners (Ubuntu 24.04) block the user namespaces Chrome's sandbox
// needs; the page is this repository's own site.
const flags = ["--headless=new", "--remote-debugging-pipe", "--no-first-run", "--no-default-browser-check", "--disable-gpu", `--user-data-dir=${profile}`];
if (process.env.CI) flags.push("--no-sandbox");
const browser = spawn(chrome(), [...flags, "about:blank"], { stdio: ["ignore", "ignore", "ignore", "pipe", "pipe"] });

// The DevTools protocol over fds 3 (to Chrome) and 4 (from Chrome), one
// NUL-terminated JSON message each.
let id = 0;
const pending = new Map();
let buf = "";
browser.stdio[4].on("data", (chunk) => {
  buf += chunk.toString();
  let i;
  while ((i = buf.indexOf("\0")) >= 0) {
    const msg = JSON.parse(buf.slice(0, i));
    buf = buf.slice(i + 1);
    const p = pending.get(msg.id);
    if (!p) continue;
    pending.delete(msg.id);
    msg.error ? p.reject(new Error(JSON.stringify(msg.error))) : p.resolve(msg.result);
  }
});
function send(method, params = {}, sessionId) {
  const msg = { id: ++id, method, params, sessionId };
  browser.stdio[3].write(JSON.stringify(msg) + "\0");
  return new Promise((resolve, reject) => pending.set(msg.id, { resolve, reject }));
}

let failures = 0;
function check(name, ok, detail = "") {
  console.log(`${ok ? "ok  " : "FAIL"} ${name}${ok || !detail ? "" : "\n" + detail}`);
  if (!ok) failures++;
}

async function page(setup = "") {
  const { targetId } = await send("Target.createTarget", { url: "about:blank" });
  const { sessionId } = await send("Target.attachToTarget", { targetId, flatten: true });
  const s = (m, p) => send(m, p, sessionId);
  await s("Page.enable");
  if (setup) await s("Page.addScriptToEvaluateOnNewDocument", { source: setup });
  const js = async (expr) => (await s("Runtime.evaluate", { expression: expr, awaitPromise: true, returnByValue: true })).result.value;
  const until = async (expr, what) => {
    for (let t = 0; t < 300; t++) {
      if (await js(expr)) return true;
      await new Promise((r) => setTimeout(r, 100));
    }
    throw new Error("timed out waiting for " + what);
  };
  return { s, js, until };
}

// With SCREENSHOTS set, the "see it run" section in both schemes and widths.
async function screenshots(p, name) {
  if (!process.env.SCREENSHOTS) return;
  for (const scheme of ["light", "dark"]) {
    for (const width of [1440, 390]) {
      await p.s("Emulation.setDeviceMetricsOverride", { width, height: 900, deviceScaleFactor: 1, mobile: width < 600 });
      await p.s("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: scheme }] });
      const r = await p.js(`scrollTo(0, 0); document.querySelectorAll(".aval-term__out").forEach((e) => (e.scrollTop = e.scrollHeight)); new Promise((ok) => setTimeout(() => { const r = document.querySelector(".aval-run").getBoundingClientRect(); ok({ y: r.top + scrollY, h: r.height }); }, 300))`);
      const { data } = await p.s("Page.captureScreenshot", { format: "png", captureBeyondViewport: true, clip: { x: 0, y: r.y, width, height: r.h, scale: 1 } });
      writeFileSync(join(process.env.SCREENSHOTS, `${name}-${scheme}-${width}.png`), Buffer.from(data, "base64"));
    }
  }
  await p.s("Emulation.clearDeviceMetricsOverride");
}

// A real mouse click in the middle of the element.
async function click(p, sel) {
  const { x, y, onTerm } = await p.js(`(() => { const e = document.querySelector(${JSON.stringify(sel)}); e.scrollIntoView({ block: "center" }); const r = e.getBoundingClientRect(); const x = r.left + r.width / 2, y = r.top + r.height / 2; const t = document.elementFromPoint(x, y); return { x, y, onTerm: !!(t && t.closest(".aval-term pre")) }; })()`);
  if (!onTerm) throw new Error("click target is not the terminal body");
  for (const type of ["mousePressed", "mouseReleased"]) await p.s("Input.dispatchMouseEvent", { type, x, y, button: "left", clickCount: 1 });
}

const enter = async (p) => {
  await p.s("Input.dispatchKeyEvent", { type: "keyDown", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, text: "\r" });
  await p.s("Input.dispatchKeyEvent", { type: "keyUp", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13 });
};

const fetched = `performance.getEntriesByType("resource").some((e) => /\\/assets\\/demo\\/(aval\\.wasm|wasm_exec\\.js)$/.test(e.name))`;
const termText = `document.querySelector(".aval-term pre").textContent`;

try {
  // The demo: lazy, then the real CLI's outcomes.
  const p = await page();
  await p.s("Page.navigate", { url: site });
  await p.until(`document.readyState === "complete" && !!document.querySelector(".aval-term__try:not([hidden])")`, "the Try it button");
  check("the static transcript is shown before the click", (await p.js(termText)).includes("reused passed result"));
  check("the terminal invites a click", /(click|tap) to run Aval live/.test(await p.js(`document.querySelector(".aval-term__cta:not([hidden])")?.textContent`)));
  check("nothing is fetched before the click", !(await p.js(fetched)));
  await screenshots(p, "before");
  await click(p, ".aval-term pre");
  await p.until(`!!document.querySelector(".aval-term__in input")`, "the prompt");
  check("aval.wasm is fetched after a click on the terminal", await p.js(fetched));
  check("the prompt takes focus and the hint is gone", await p.js(`document.activeElement === document.querySelector(".aval-term__in input") && !document.querySelector(".aval-term__cta")`));
  const type = async (line) => {
    await p.js(`document.querySelector(".aval-term__in input").focus()`);
    await p.s("Input.insertText", { text: line });
    await enter(p);
    return p.js(termText);
  };
  let out = await type("aval run");
  check("first aval run misses, runs and pushes", /miss greet[\s\S]*ran greet: passed \(exit 0\)[\s\S]*pushed /.test(out), out);
  out = await type("aval run");
  check("second aval run is verified and reused", /verified greet[\s\S]*reused passed result\n$/.test(out), out);
  await screenshots(p, "after");
  await type("tamper");
  out = await type("aval verify");
  check("tamper then aval verify is verification-failed, exit 5", /aval: SECURITY[\s\S]*verification-failed greet[\s\S]*\nexit 5\n$/.test(out), out);

  // Without WebAssembly the transcript stays and the page says why.
  const q = await page("delete window.WebAssembly;");
  await q.s("Page.navigate", { url: site });
  await q.until(`document.readyState === "complete" && !!document.querySelector(".aval-term__try:not([hidden])")`, "the Try it button");
  await q.js(`document.querySelector(".aval-term pre").focus()`);
  await enter(q);
  await q.until(`document.querySelector(".aval-try__status").textContent !== ""`, "the fallback message");
  check("without WebAssembly the transcript stays", (await q.js(termText)).includes("reused passed result") && (await q.js(`document.querySelector(".aval-try__status").textContent`)).includes("cannot run WebAssembly"));
} catch (e) {
  check("smoke test", false, e.stack);
} finally {
  const exited = browser.exitCode === null ? new Promise((r) => browser.once("exit", r)) : null;
  browser.kill();
  await exited;
  server.close();
  rmSync(profile, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 });
}
process.exit(failures ? 1 : 0);
