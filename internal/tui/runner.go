package tui

import (
	"fmt"
	"os"
)

func PromptContinue(message string) bool {
	if !IsInteractive() {
		return true
	}

	fmt.Fprintf(os.Stderr, "%s [Y/n]: ", message)

	var response string
	if _, err := fmt.Scanln(&response); err != nil {
		return true
	}

	return response == "" || response == "y" || response == "Y"
}
