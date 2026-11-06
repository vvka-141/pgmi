package params

import (
	"maps"
	"strings"
	"testing"
)

func TestFromEnviron(t *testing.T) {
	tests := []struct {
		name    string
		environ []string
		want    map[string]string
		wantErr string
	}{
		{
			name:    "imports only the prefix",
			environ: []string{"PATH=/bin", "GITHUB_TOKEN=gh", "PGMI_PARAMX=no", "pgmi_param_lower=no", "PGMI_PARAM_DB_PASSWORD=s3cret"},
			want:    map[string]string{"DB_PASSWORD": "s3cret"},
		},
		{
			name:    "value is taken verbatim",
			environ: []string{`PGMI_PARAM_DSN= a=b "c" `},
			want:    map[string]string{"DSN": ` a=b "c" `},
		},
		{
			name: "nothing set",
			want: map[string]string{},
		},
		{
			name:    "empty value is refused",
			environ: []string{"PGMI_PARAM_OK=1", "PGMI_PARAM_API_KEY="},
			wantErr: "PGMI_PARAM_API_KEY",
		},
		{
			name:    "empty name is refused",
			environ: []string{"PGMI_PARAM_=v"},
			wantErr: "PGMI_PARAM_",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FromEnviron(tt.environ)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("want error naming %s, got %v", tt.wantErr, got)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error %q does not name %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}
