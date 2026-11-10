# Records docs/avocado-park-demo.gif with made-up sessions (scripts/demo).
# Run from the repo root: vhs scripts/demo/demo.tape   (brew install vhs)

Output docs/avocado-park-demo.gif

Set Shell zsh
Set FontSize 16
Set Width 1080
Set Height 440
Set Padding 18
Set Theme "Dracula"
Set TypingSpeed 90ms
Set CursorBlink false

Hide
Type "go build -o /tmp/avocado-park-demo ./scripts/demo && clear && /tmp/avocado-park-demo"
Enter
Sleep 3s
Show

# The list: memory per session, purple idle times past 3 days, parked rows.
Sleep 3s

# One workspace at a time, then back to all.
Type "w"
Sleep 1.3s
Type "w"
Sleep 1.3s
Type "w"
Sleep 1.3s
Escape
Sleep 800ms

# Filter by any name.
Type "/"
Sleep 400ms
Type "helm"
Sleep 1.5s
Escape
Sleep 1s

# Park everything idle 3+ days.
Type "P"
Sleep 2.2s
Type "y"
Sleep 3.5s

# Park a whole tab: release-notes and changelog-bot share one.
Type "g"
Sleep 400ms
Down
Sleep 800ms
Type "T"
Sleep 2.2s
Type "y"
Sleep 3.5s

# Resume a parked session in its pane.
Type "G"
Sleep 1s
Type "u"
Sleep 3.5s

Type "q"
Sleep 300ms

Hide
Type "rm -f /tmp/avocado-park-demo"
Enter
