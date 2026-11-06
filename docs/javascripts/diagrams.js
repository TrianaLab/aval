// Renders ```mermaid fences with the Mermaid copy vendored in vendor/ (pinned
// version, licence and update steps: TestVendoredMermaid in
// tests/architecture/docs_test.go). mkdocs.yml gives those fences the class aval-mermaid,
// not Material's "mermaid", so Material's own integration, which falls back
// to a CDN, never runs. Mermaid is fetched only on a page that has a
// diagram, with Subresource Integrity: tests/architecture/docs_test.go
// checks that INTEGRITY is the vendored file's hash.
//
// Colours are CSS variables from stylesheets/extra.css, written into the
// diagram's own CSS, so a diagram follows the light/dark toggle without
// being rendered again. Only flowcharts are themed.
(function () {
  var SRC = new URL("vendor/mermaid.min.js", document.currentScript.src).href;
  var INTEGRITY = "sha384-EOXBFmc3gx5mb+vn0vPvvGqACToJD24hhacX5Yx+8NUUQrHIle/Qi5Bg9o3zKwW2";
  var FONT = '"Manrope", system-ui, sans-serif';
  var CSS = [
    "{font-family:" + FONT + "}",
    ".node rect,.node polygon,.node circle,.node path{fill:var(--c-bg);stroke:var(--c-accent);stroke-width:1.5px}",
    ".nodeLabel,.nodeLabel p,.label{color:var(--c-text);fill:var(--c-text)}",
    ".flowchart-link{stroke:var(--c-text-3)}",
    ".marker{fill:var(--c-text-3);stroke:var(--c-text-3)}",
    ".edgeLabel,.edgeLabel p,.edgeLabel rect,.labelBkg{background-color:var(--c-bg);fill:var(--c-bg);color:var(--c-text-2)}",
    ".cluster rect{fill:var(--c-bg-2);stroke:var(--c-border)}",
    ".cluster-label .nodeLabel,.cluster-label span{color:var(--c-text-3)}",
    // The diagram vocabulary (docs/brand.md): ok, warn, err, muted.
    ".node.ok rect,.node.ok polygon,.node.ok circle,.node.ok path{fill:var(--c-accent-soft);stroke:var(--c-accent)}",
    ".node.warn rect,.node.warn polygon,.node.warn circle,.node.warn path{stroke:var(--c-warn)}",
    ".node.err rect,.node.err polygon,.node.err circle,.node.err path{stroke:var(--c-err);stroke-width:2px}",
    ".node.muted rect,.node.muted polygon,.node.muted circle,.node.muted path{fill:var(--c-bg-2);stroke:var(--c-border)}",
  ].join("");
  var loading;

  function load() {
    loading =
      loading ||
      new Promise(function (resolve, reject) {
        var s = document.createElement("script");
        s.src = SRC;
        s.integrity = INTEGRITY;
        s.crossOrigin = "anonymous";
        s.onload = function () {
          window.mermaid.initialize({
            startOnLoad: false,
            securityLevel: "strict",
            theme: "base",
            themeVariables: { fontFamily: FONT, fontSize: "16px" },
            themeCSS: CSS,
            flowchart: { curve: "basis", padding: 12, nodeSpacing: 28, rankSpacing: 36 },
          });
          resolve(window.mermaid);
        };
        s.onerror = reject;
        document.head.appendChild(s);
      });
    return loading;
  }

  var n = 0;
  function render() {
    var blocks = document.querySelectorAll("pre.aval-mermaid");
    if (!blocks.length) return;
    // Text is measured at render time: wait for the bundled fonts.
    Promise.all([load(), document.fonts.ready]).then(function (r) {
      blocks.forEach(function (pre) {
        r[0].render("aval-diagram-" + n++, pre.textContent).then(function (out) {
          var fig = document.createElement("div");
          fig.className = "aval-diagram";
          fig.innerHTML = out.svg;
          pre.replaceWith(fig);
        });
      });
    });
  }

  // document$ (Material) emits on the first load and after every instant
  // navigation.
  window.document$.subscribe(render);
})();
