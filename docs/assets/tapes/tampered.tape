# Tampered attestation: a passing result signed with a key the policy does not
# trust is rejected, the test runs anyway and aval exits 5. Rendered by
# `make docs-demos` after changed-input.tape (see miss.tape).

Output docs/assets/gifs/tampered.gif

Set Shell bash
Set Theme "Catppuccin Mocha"
Set FontSize 14
Set Width 1180
Set Height 470
Set Padding 16
Set Framerate 15
Set TypingSpeed 35ms
Set CursorBlink false

Hide
Type `export PS1='$ ' PATH="$AVAL_DEMO_BIN:$PATH" AVAL_REGISTRY=localhost:5001/demo/hello-aval AVAL_POLICY=policy.yaml && cd "$AVAL_DEMO_DIR" && clear` Enter
Sleep 500ms
Show

Type "AVAL_POLICY= aval run --push --key other.key"
Sleep 500ms
Enter
Sleep 3s
Type `aval run; echo "exit code: $?"`
Sleep 500ms
Enter
Sleep 6s
