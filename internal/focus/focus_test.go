package focus

import (
	"errors"
	"strings"
	"testing"
)

func TestResolvePath(t *testing.T) {
	appMapping := map[string]string{
		"Google Chrome": "browser",
		"iTerm2":        "terminal",
	}

	tests := []struct {
		name       string
		activeApp  string
		appErr     error
		id         string
		wantResult string
		wantErr    string
	}{
		{
			name:       "known app, regular ID",
			activeApp:  "Google Chrome",
			id:         "tabs/1",
			wantResult: "browser/tabs/1",
		},
		{
			name:       "known app, ID with leading slash",
			activeApp:  "iTerm2",
			id:         "/sessions/1",
			wantResult: "terminal/sessions/1",
		},
		{
			name:       "known app, empty ID (root)",
			activeApp:  "Google Chrome",
			id:         "",
			wantResult: "browser",
		},
		{
			name:      "unknown app",
			activeApp: "Finder",
			id:        "files/1",
			wantErr:   `focus: active app "Finder" is not supported`,
		},
		{
			name:    "app provider error",
			appErr:  errors.New("osascript failed"),
			id:      "anything",
			wantErr: "focus: osascript failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Router{
				appMapping: appMapping,
				getApp: func() (string, error) {
					if tt.appErr != nil {
						return "", tt.appErr
					}
					return tt.activeApp, nil
				},
			}

			gotResult, gotErr := r.resolvePath(tt.id)

			if tt.wantErr != "" {
				if gotErr == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(gotErr.Error(), tt.wantErr) {
					t.Errorf("expected error containing %q, got %q", tt.wantErr, gotErr.Error())
				}
				return
			}

			if gotErr != nil {
				t.Fatalf("unexpected error: %v", gotErr)
			}

			if gotResult != tt.wantResult {
				t.Errorf("resolvePath() = %q, want %q", gotResult, tt.wantResult)
			}
		})
	}
}
