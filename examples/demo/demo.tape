# The GIF at the top of pit's README. Regenerate it from the repository's
# root with:
#
#   vhs examples/demo/demo.tape
#
# It builds pit from this checkout, sets up the demo in a temporary
# directory with a state directory of its own, and warms the build cache
# before recording, so the GIF shows a second run rather than a download.

Output examples/demo/demo.gif

Require go
Require docker
Require git

Set Shell bash
Set FontSize 15
Set Width 1280
Set Height 760
Set Padding 24
Set TypingSpeed 45ms
Set WaitTimeout 600s
Set WaitPattern /\$ ?$/
Set Theme "Catppuccin Mocha"

Hide
Type "export PS1='$ ' XDG_STATE_HOME=$(mktemp -d) BIN=$(mktemp -d)" Enter
Type "go build -o $BIN/pit ./cmd/pit && export PATH=$BIN:$PATH" Enter
Wait
Type "D=$(mktemp -d)/pit-demo && examples/demo/setup.sh $D >/dev/null && cd $D/teashop" Enter
Wait
Type "pit 7 >/dev/null 2>&1; pit down 7 >/dev/null 2>&1; clear" Enter
Wait
Show

Type "pit 7 --scenario refunded" Sleep 400ms Enter
Wait
Sleep 2s

Hide
Type "clear" Enter
Show
Type "pit what 7" Sleep 400ms Enter
Wait
Sleep 6s

Type "pit ls" Sleep 400ms Enter
Wait
Sleep 3s

Type "pit down 7" Sleep 400ms Enter
Wait
Sleep 3s

Hide
Type "rm -rf $D $XDG_STATE_HOME $BIN" Enter
