// "Try it" in the home page terminal (overrides/home.html): the real Aval
// compiled to WebAssembly (cmd/aval-wasm, built by `make docs-wasm` into
// assets/demo/). Until the visitor clicks the terminal (or its button) or
// presses a key in it, the terminal shows the static transcript and nothing
// is fetched. wasm_exec.js is vendored next to the
// wasm and pinned to the Go toolchain that builds it (tests/architecture).
// If WebAssembly is unavailable or loading fails, the transcript stays.
// Output is inserted as text, never as HTML.
(function () {
  var BASE = new URL("../assets/demo/", document.currentScript.src).href;
  var loading;

  function load() {
    loading =
      loading ||
      new Promise(function (resolve, reject) {
        if (window.avalDemo) return resolve();
        if (typeof WebAssembly !== "object") return reject(new Error("this browser cannot run WebAssembly"));
        var s = document.createElement("script");
        s.src = BASE + "wasm_exec.js";
        s.onerror = function () {
          reject(new Error("wasm_exec.js did not load"));
        };
        s.onload = function () {
          var go = new window.Go();
          window.onAvalDemoReady = resolve;
          fetch(BASE + "aval.wasm")
            .then(function (r) {
              if (!r.ok) throw new Error("aval.wasm: HTTP " + r.status);
              return r.arrayBuffer();
            })
            .then(function (buf) {
              return WebAssembly.instantiate(buf, go.importObject);
            })
            .then(function (m) {
              go.run(m.instance); // resolves through onAvalDemoReady
            })
            .catch(reject);
        };
        document.head.appendChild(s);
      });
    loading.catch(function () {
      loading = null; // let a later click retry
    });
    return loading;
  }

  // The class of an output line, for the transcript's colours.
  function tone(line) {
    if (/^(verified |reused |pushed )/.test(line)) return "g";
    if (/^miss /.test(line)) return "y";
    if (/^(verification-failed |aval: SECURITY|rejected: )/.test(line)) return "r";
    if (/^(test |reason: |signer: |reference: |aval-fingerprint|contract |source |dependency |toolchain |env )/.test(line)) return "d";
    return "";
  }

  function print(pre, text, cls) {
    text.replace(/\n$/, "").split("\n").forEach(function (line) {
      var span = document.createElement("span");
      span.className = cls || tone(line);
      span.textContent = line + "\n";
      pre.appendChild(span);
    });
  }

  var LABEL = "\u25B6 Try it live";

  // start loads the demo once; first is a key the visitor typed in the
  // terminal to start it, carried into the prompt.
  function start(fig, btn, note, first) {
    if (fig.dataset.state) return;
    fig.dataset.state = "loading";
    var pre = fig.querySelector("pre");
    var cta = fig.querySelector(".aval-term__cta");
    btn.disabled = true;
    btn.textContent = "Loading\u2026";
    cta.textContent = "  loading Aval\u2026";
    note.textContent = "";
    load().then(
      function () {
        fig.dataset.state = "live";
        btn.hidden = true;
        pre.textContent = "";
        pre.removeAttribute("tabindex");
        pre.setAttribute("aria-live", "polite");
        pre.classList.add("aval-term__out");
        print(pre, "Aval is running in your browser. Type help, or start with: aval run", "c");
        var form = document.createElement("form");
        form.className = "aval-term__in";
        form.innerHTML =
          '<label><span class="pr" aria-hidden="true">$</span>' +
          '<input type="text" aria-label="Command" autocomplete="off" autocapitalize="off" spellcheck="false" enterkeyhint="send"></label>';
        pre.after(form);
        var input = form.querySelector("input");
        var history = [];
        var at = 0;
        form.addEventListener("submit", function (e) {
          e.preventDefault();
          var line = input.value.trim();
          input.value = "";
          if (line) history.push(line);
          at = history.length;
          if (line === "clear") {
            pre.textContent = "";
            return;
          }
          print(pre, "$ " + line, "w");
          var r = window.avalDemo(line);
          if (r.output) print(pre, r.output);
          if (r.code !== 0) print(pre, "exit " + r.code, r.code === 5 ? "r" : "d");
          pre.scrollTop = pre.scrollHeight;
        });
        input.addEventListener("keydown", function (e) {
          if (e.key !== "ArrowUp" && e.key !== "ArrowDown") return;
          e.preventDefault();
          at = Math.max(0, Math.min(history.length, at + (e.key === "ArrowUp" ? -1 : 1)));
          input.value = history[at] || "";
        });
        // A click in the output, unless it selects text, goes to the prompt.
        pre.addEventListener("click", function () {
          if (!String(window.getSelection())) input.focus();
        });
        if (first) input.value = first;
        input.focus();
      },
      function (err) {
        delete fig.dataset.state;
        btn.disabled = false;
        btn.textContent = LABEL;
        cta.textContent = hint();
        note.textContent = "The demo could not start (" + err.message + "). The transcript above is real output.";
      }
    );
  }

  function hint() {
    var verb = window.matchMedia("(hover: none)").matches ? "tap" : "click";
    return "  \u2190 " + verb + " to run Aval live in your browser";
  }

  function init() {
    var fig = document.querySelector(".aval-term[data-try]");
    if (!fig || fig.dataset.ready) return;
    fig.dataset.ready = "1";
    var btn = fig.querySelector(".aval-term__try");
    var note = document.querySelector(".aval-try__status");
    var pre = fig.querySelector("pre");
    var cta = fig.querySelector(".aval-term__cta");
    var go = function (first) {
      start(fig, btn, note, first);
    };
    btn.hidden = false;
    cta.textContent = hint();
    cta.hidden = false;
    // The whole terminal body is the entry: click it, or focus it and press
    // Enter, Space or any character (which then lands in the prompt).
    pre.tabIndex = 0;
    pre.addEventListener("click", function () {
      if (!fig.dataset.state && !String(window.getSelection())) go();
    });
    pre.addEventListener("keydown", function (e) {
      if (fig.dataset.state || e.ctrlKey || e.metaKey || e.altKey) return;
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        go();
      } else if (e.key.length === 1) {
        e.preventDefault();
        go(e.key);
      }
    });
    btn.addEventListener("click", function () {
      go();
    });
  }

  // document$ (Material) emits on the first load and after every instant
  // navigation.
  window.document$.subscribe(init);
})();
