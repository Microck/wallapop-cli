package cli

import "context"

// UsePromptTerminal makes the login's renewal prompt see a terminal (or not).
func UsePromptTerminal(tty bool) (restore func()) {
	prev := promptTerminal
	promptTerminal = func(*App) bool { return tty }
	return func() { promptTerminal = prev }
}

// UseServiceExec runs service manager commands through fn instead of the OS.
func UseServiceExec(fn func(name string, args ...string) error) (restore func()) {
	prev := serviceExec
	serviceExec = func(_ context.Context, name string, args ...string) error { return fn(name, args...) }
	return func() { serviceExec = prev }
}

// UseBundledEngine lets `auth service enable` treat the test's external
// daemon (WALLAPOP_GALLETON_*) as the bundled engine.
func UseBundledEngine() (restore func()) {
	prev := externalEngine
	externalEngine = func() bool { return false }
	return func() { externalEngine = prev }
}
