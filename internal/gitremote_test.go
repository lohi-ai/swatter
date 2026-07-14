package internal

import "testing"

func TestParseRemoteURL(t *testing.T) {
	cases := []struct {
		in         string
		owner, rep string
		wantErr    bool
	}{
		{"git@github.com:lohi-ai/swatter.git", "lohi-ai", "swatter", false},
		{"git@github.com:lohi-ai/swatter", "lohi-ai", "swatter", false},
		{"https://github.com/lohi-ai/swatter.git", "lohi-ai", "swatter", false},
		{"https://github.com/lohi-ai/swatter", "lohi-ai", "swatter", false},
		{"https://github.com/lohi-ai/swatter/", "lohi-ai", "swatter", false},
		{"ssh://git@github.com/lohi-ai/swatter.git", "lohi-ai", "swatter", false},
		{"ssh://git@github.example.com:2222/lohi-ai/swatter.git", "lohi-ai", "swatter", false},
		{"  https://github.com/lohi-ai/swatter.git  ", "lohi-ai", "swatter", false},
		{"", "", "", true},
		{"notaurl", "", "", true},
		{"https://github.com/onlyowner", "", "", true},
	}
	for _, c := range cases {
		o, r, err := ParseRemoteURL(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseRemoteURL(%q) = %q/%q, want error", c.in, o, r)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRemoteURL(%q) error: %v", c.in, err)
			continue
		}
		if o != c.owner || r != c.rep {
			t.Errorf("ParseRemoteURL(%q) = %q/%q, want %q/%q", c.in, o, r, c.owner, c.rep)
		}
	}
}
