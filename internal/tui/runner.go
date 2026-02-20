package tui

import "fmt"

func PromptContinue(message string) bool {
	if !IsInteractive() {
		return true
	}

	fmt.Printf("%s [Y/n]: ", message)

	var response string
	if _, err := fmt.Scanln(&response); err != nil {
		return true
	}

	return response == "" || response == "y" || response == "Y"
}
