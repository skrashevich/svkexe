# Run with: bash docs/demo/serve.sh record
Output docs/media/ssh-session.gif
Require ssh
Set Shell "bash"
Set FontFamily "monospace"
Set FontSize 18
Set Width 1100
Set Height 680
Set Padding 24
Set Theme "Catppuccin Mocha"
Set WindowBar Colorful
Set TypingSpeed 45ms
Set Framerate 20
Hide
Type `export PS1='$ '; function ssh() { command ssh -F "$SVKEXE_DEMO_SSH_CONFIG" "$@"; }; clear`
Enter
Sleep 500ms
Show
Type "ssh svkexe-demo"
Enter
Sleep 2s
Type "ls"
Enter
Sleep 3s
Type "stat dev"
Enter
Sleep 5s
Type "exit"
Enter
Sleep 1s
Ctrl+L
Type `ssh svkexe-demo "stat dev --json"`
Enter
Sleep 6s
