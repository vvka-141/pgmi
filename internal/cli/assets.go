package cli

import _ "embed"

// ASCII art assets embedded at compile time

//go:embed assets/logo.txt
var asciiLogo string
