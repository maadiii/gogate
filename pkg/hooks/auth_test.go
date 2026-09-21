package hooks

import "testing"

// TestBearerToken pins down the header parsing on its own, because this is
// where the hook's one silent-failure mode lives: a credential that never
// reaches the parser looks exactly like a bad credential from the outside, so
// a parsing regression here reads as "auth rejects everything" rather than as
// a crash.
func TestBearerToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		header string
		want   string
		wantOK bool
	}{
		{name: "well formed", header: "Bearer abc123", want: "abc123", wantOK: true},
		{name: "lowercase scheme", header: "bearer abc123", want: "abc123", wantOK: true},
		{name: "uppercase scheme", header: "BEARER abc123", want: "abc123", wantOK: true},
		{name: "mixed case scheme", header: "BeArEr abc123", want: "abc123", wantOK: true},
		{name: "extra spaces after scheme", header: "Bearer   abc123", want: "abc123", wantOK: true},
		{name: "surrounding whitespace", header: "  Bearer abc123  ", want: "abc123", wantOK: true},
		{name: "scheme only", header: "Bearer", want: "", wantOK: false},
		{name: "scheme and spaces", header: "Bearer   ", want: "", wantOK: false},
		{name: "no scheme", header: "abc123", want: "", wantOK: false},
		{name: "empty header", header: "", want: "", wantOK: false},
		{name: "whitespace header", header: "   ", want: "", wantOK: false},
		{name: "wrong scheme", header: "Basic abc123", want: "", wantOK: false},
		{name: "scheme as substring", header: "Bearerabc123", want: "", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := bearerToken([]byte(tt.header))
			if ok != tt.wantOK {
				t.Fatalf("bearerToken(%q) ok = %v, want %v", tt.header, ok, tt.wantOK)
			}

			if got != tt.want {
				t.Errorf("bearerToken(%q) = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}
