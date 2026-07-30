//go:build pgmi_testhooks

package main

import "os"

func triggerTestPanic() {
	if os.Getenv("PGMI_TEST_PANIC") == "1" {
		panic("intentional test panic")
	}
}
