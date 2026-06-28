package microvm

import "testing"

func TestShellQuote(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{"echo", "hi"}, `'echo' 'hi'`},
		{[]string{"echo", "a b"}, `'echo' 'a b'`}, // spaces stay in one arg
		{[]string{"sh", "-c", "exit 7"}, `'sh' '-c' 'exit 7'`},
		{[]string{"echo", "it's"}, `'echo' 'it'\''s'`},                         // embedded single quote escaped
		{[]string{"rm", "-rf", "$HOME; reboot"}, `'rm' '-rf' '$HOME; reboot'`}, // metachars inert
	} {
		if got := shellQuote(c.in); got != c.want {
			t.Errorf("shellQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
