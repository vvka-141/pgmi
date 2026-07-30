package tui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

func PromptContinue(message string) bool {
	if !IsInteractive() {
		return true
	}
	return promptContinueFromStdin(message)
}

func promptContinueFromStdin(message string) bool {
	fmt.Fprintf(os.Stderr, "%s [Y/n]: ", message)

	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false
	}

	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "" || answer == "y" || answer == "yes"
}
