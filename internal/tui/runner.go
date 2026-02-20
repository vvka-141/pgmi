package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

func PromptContinue(message string) bool {
	if !IsInteractive() {
		return true
	}

	fmt.Printf("%s [Y/n]: ", message)

	var response string
	fmt.Scanln(&response)

	return response == "" || response == "y" || response == "Y"
}

type ProgressDisplay struct {
	program *tea.Program
}

func NewProgressDisplay() *ProgressDisplay {
	return &ProgressDisplay{}
}

func (p *ProgressDisplay) Start(message string) {
	if !IsInteractive() {
		fmt.Println(message)
		return
	}
	fmt.Printf("◐ %s\n", message)
}

func (p *ProgressDisplay) Success(message string) {
	fmt.Printf("✓ %s\n", message)
}

func (p *ProgressDisplay) Error(message string) {
	fmt.Printf("✗ %s\n", message)
}
