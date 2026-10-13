package main

import "testing"

func TestResolveMode(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{"no arg defaults to serve", nil, "serve", false},
		{"serve", []string{"serve"}, "serve", false},
		{"cronjob", []string{"cronjob"}, "cronjob", false},
		{"unknown mode", []string{"once"}, "", true},
		{"retired polling name is rejected", []string{"polling"}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveMode(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveMode(%v): expected an error, got %q", tc.args, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveMode(%v): %v", tc.args, err)
			}
			if got != tc.want {
				t.Fatalf("resolveMode(%v) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}
