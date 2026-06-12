package console

import "testing"

func TestParseExitMarker(t *testing.T) {
	tests := []struct {
		name         string
		output       string
		wantStdout   string
		wantExitCode int
	}{
		{
			name:         "zero exit",
			output:       "hello world\n" + aciExitMarkerPrefix + "0" + aciExitMarkerSuffix,
			wantStdout:   "hello world\n",
			wantExitCode: 0,
		},
		{
			name:         "nonzero exit",
			output:       "boom\n" + aciExitMarkerPrefix + "127" + aciExitMarkerSuffix,
			wantStdout:   "boom\n",
			wantExitCode: 127,
		},
		{
			name:         "trailing newline after marker is ignored",
			output:       "ok\n" + aciExitMarkerPrefix + "1" + aciExitMarkerSuffix + "\n",
			wantStdout:   "ok\n",
			wantExitCode: 1,
		},
		{
			name:         "missing marker returns full output and exit 0",
			output:       "no marker here",
			wantStdout:   "no marker here",
			wantExitCode: 0,
		},
		{
			name:         "unterminated marker returns full output and exit 0",
			output:       "partial" + aciExitMarkerPrefix + "0",
			wantStdout:   "partial" + aciExitMarkerPrefix + "0",
			wantExitCode: 0,
		},
		{
			name:         "non-numeric code returns exit 0",
			output:       "weird\n" + aciExitMarkerPrefix + "??" + aciExitMarkerSuffix,
			wantStdout:   "weird\n",
			wantExitCode: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stdout, exitCode := parseExitMarker(tt.output)
			if stdout != tt.wantStdout {
				t.Errorf("stdout = %q, want %q", stdout, tt.wantStdout)
			}
			if exitCode != tt.wantExitCode {
				t.Errorf("exitCode = %d, want %d", exitCode, tt.wantExitCode)
			}
		})
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"plain", `'plain'`},
		{"with spaces", `'with spaces'`},
		{"it's quoted", `'it'\''s quoted'`},
		{"", `''`},
	}
	for _, tt := range tests {
		if got := shellQuote(tt.in); got != tt.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
