# Second run: a trusted result for the same inputs exists, so the test is
# skipped. Rendered by `make docs-demos` after miss.tape (see miss.tape).

Output docs/assets/gifs/hit.gif

Set Shell bash
Set Theme "Catppuccin Mocha"
Set FontSize 14
Set Width 1180
Set Height 300
Set Padding 16
Set Framerate 15
Set TypingSpeed 35ms
Set CursorBlink false

Hide
Type `export PS1='$ ' PATH="$AVAL_DEMO_BIN:$PATH" AVAL_REGISTRY=localhost:5001/demo/hello-aval AVAL_POLICY=policy.yaml && cd "$AVAL_DEMO_DIR" && clear` Enter
Sleep 500ms
Show

Type "aval verify"
Sleep 500ms
Enter
Sleep 2s
Type "aval run"
Sleep 500ms
Enter
Sleep 4s
