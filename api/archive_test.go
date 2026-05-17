package api

import "testing"

func TestCleanArchivePath(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"empty", "", ".", false},
		{"root", "/", ".", false},
		{"multi slash root", "///", ".", false},
		{"absolute", "/tmp/state", "tmp/state", false},
		{"multi slash absolute", "//tmp//state", "tmp/state", false},
		{"clean", "tmp/./state", "tmp/state", false},
		{"parent", "../state", "", true},
		{"nested parent", "tmp/../state", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cleanArchivePath(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("cleanArchivePath(%q) error = nil, want error", tt.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("cleanArchivePath(%q) error = %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("cleanArchivePath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
