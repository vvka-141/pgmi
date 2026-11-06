package db

import (
	"testing"
)

func TestConnectionConfig_DeepCopy(t *testing.T) {
	t.Run("copies AdditionalParams independently", func(t *testing.T) {
		orig := ConnectionConfig{
			Host:             "localhost",
			Port:             5432,
			AdditionalParams: map[string]string{"a": "1", "b": "2"},
		}
		cp := orig.DeepCopy()

		cp.AdditionalParams["a"] = "changed"
		cp.Host = "remote"

		if orig.AdditionalParams["a"] != "1" {
			t.Error("DeepCopy did not isolate AdditionalParams map")
		}
		if orig.Host == "remote" {
			t.Error("DeepCopy did not isolate scalar fields")
		}
		if len(cp.AdditionalParams) != 2 {
			t.Errorf("expected 2 params in copy, got %d", len(cp.AdditionalParams))
		}
	})

	t.Run("nil AdditionalParams stays nil", func(t *testing.T) {
		orig := ConnectionConfig{Host: "localhost"}
		cp := orig.DeepCopy()

		if cp.AdditionalParams != nil {
			t.Error("expected nil AdditionalParams in copy")
		}
	})

	t.Run("empty AdditionalParams stays empty", func(t *testing.T) {
		orig := ConnectionConfig{
			AdditionalParams: map[string]string{},
		}
		cp := orig.DeepCopy()

		if cp.AdditionalParams == nil {
			t.Error("expected non-nil empty map in copy")
		}
		if len(cp.AdditionalParams) != 0 {
			t.Errorf("expected empty map in copy, got %d entries", len(cp.AdditionalParams))
		}
	})
}
