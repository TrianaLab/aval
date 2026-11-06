# First run: no result for these inputs yet, so the test runs and the signed
# result is pushed. Rendered by `make docs-demos`, which builds bin/aval,
# starts a registry on localhost:5001, runs setup.sh in AVAL_DEMO_DIR and the
# tapes in order (miss, hit, changed-input, tampered); each starts where the
# previous one stopped. The text alternative is docs/quickstart.md.

Output docs/assets/gifs/miss.gif

Set Shell bash
Set Theme "Catppuccin Mocha"
Set FontSize 14
Set Width 980
Set Height 260
Set Padding 16
Set Framerate 15
Set TypingSpeed 35ms
Set CursorBlink false

Hide
Type `export PS1='$ ' PATH="$AVAL_DEMO_BIN:$PATH" && cd "$AVAL_DEMO_DIR" && clear` Enter
Sleep 500ms
Show

Type "aval run --push --key aval.key --registry localhost:5001/demo/hello-aval"
Sleep 500ms
Enter
Sleep 4s
