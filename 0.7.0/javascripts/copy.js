// Copy buttons outside code blocks (the home page install command): a click
// on [data-copy] puts its text on the clipboard. Delegated on document, so it
// keeps working after instant navigation swaps the page.
document.addEventListener("click", function (e) {
  var b = e.target.closest && e.target.closest("[data-copy]");
  if (!b || !navigator.clipboard) return;
  navigator.clipboard.writeText(b.getAttribute("data-copy")).then(function () {
    b.setAttribute("data-copied", "");
    b.title = "Copied";
    setTimeout(function () {
      b.removeAttribute("data-copied");
      b.title = "Copy";
    }, 1500);
  });
});
