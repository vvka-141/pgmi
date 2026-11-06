package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/vvka-141/pgmi/pkg/pgmi"
)

type refusingTokenProvider struct{}

func (refusingTokenProvider) String() string { return "refusing" }

func (refusingTokenProvider) GetToken(context.Context) (string, time.Time, error) {
	return "", time.Time{}, errors.New("credentials expired")
}

func TestTokenConnector_ATokenFailureIsAConnectionFailure(t *testing.T) {
	c := NewTokenBasedConnector(&ConnectionConfig{Host: "db.example", Port: 5432, Database: "app"},
		refusingTokenProvider{}, "AWS", Output{})
	_, err := c.Connect(context.Background())
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitConnectionError {
		t.Errorf("exit code %d, want %d: %v", got, pgmi.ExitConnectionError, err)
	}
}
