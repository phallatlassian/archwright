package session

import (
	"os"
	"testing"
)

// The test binary doubles as the hook command: a Session's hooks run
// `session.test` with ARCHWRIGHT_TEST_HOOK=1 and deliver what they read on
// stdin through SendHook, the real hook client.
func TestMain(m *testing.M) {
	if os.Getenv("ARCHWRIGHT_TEST_HOOK") == "1" {
		getenv := os.Getenv
		if os.Getenv("ARCHWRIGHT_TEST_NO_SOCKET") == "1" { // the Works' receiver is unreachable: only the spool is left
			getenv = func(k string) string {
				if k == EnvHookSocket {
					return ""
				}
				return os.Getenv(k)
			}
		}
		SendHook(getenv, os.Stdin)
		os.Exit(0) // a hook never fails the CLI
	}
	os.Exit(m.Run())
}
