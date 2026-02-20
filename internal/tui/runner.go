package tui

import "fmt"

func PromptContinue(message string) bool {
	if !IsInteractive() {
		return true
	}

	fmt.Printf("%s [Y/n]: ", message)

	var response string
	fmt.Scanln(&response)

	return response == "" || response == "y" || response == "Y"
}
