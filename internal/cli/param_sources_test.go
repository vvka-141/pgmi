package cli

import (
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vvka-141/pgmi/internal/config"
	"github.com/vvka-141/pgmi/internal/files/fakefs"
	"github.com/vvka-141/pgmi/pkg/pgmi"
)

func TestLoadMergedParameters_Precedence(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "p.env")
	if err := os.WriteFile(file, []byte("from_file=file\nfrom_flag=file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.ProjectConfig{Params: map[string]string{
		"from_yaml": "yaml", "from_env": "yaml", "from_file": "yaml", "from_flag": "yaml",
	}}
	got, err := loadMergedParameters(cfg, paramSources{
		environ: []string{"PGMI_PARAM_FROM_ENV=env", "PGMI_PARAM_FROM_FILE=env", "PGMI_PARAM_FROM_FLAG=env", "UNRELATED_SECRET=x"},
		files:   []string{file},
		pairs:   []string{"from_flag=flag"},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"from_yaml": "yaml", "from_env": "env", "from_file": "file", "from_flag": "flag"}
	if !maps.Equal(got, want) {
		t.Errorf("merged = %v, want %v", got, want)
	}
}

func TestLoadMergedParameters_EmptyEnvValueIsInvalidConfig(t *testing.T) {
	_, err := loadMergedParameters(nil, paramSources{environ: []string{"PGMI_PARAM_DB_PASSWORD="}}, false)
	if !errors.Is(err, pgmi.ErrInvalidConfig) {
		t.Fatalf("want ErrInvalidConfig (exit 10), got %v", err)
	}
	if !strings.Contains(err.Error(), "PGMI_PARAM_DB_PASSWORD") {
		t.Errorf("error should name the variable: %v", err)
	}
}

func TestLoadMergedParameters_EnvCaseVariantsAreInvalidConfig(t *testing.T) {
	_, err := loadMergedParameters(nil, paramSources{environ: []string{"PGMI_PARAM_Region=eu", "PGMI_PARAM_REGION=us"}}, false)
	if !errors.Is(err, pgmi.ErrInvalidConfig) {
		t.Fatalf("want ErrInvalidConfig (exit 10), got %v", err)
	}
}

func TestLoadMergedParameters_VerboseEnvOmitsValues(t *testing.T) {
	output := captureStderr(t, func() {
		if _, err := loadMergedParameters(nil, paramSources{environ: []string{"PGMI_PARAM_API_KEY=SENTINEL_ENV"}}, true); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(output, "1 parameter(s) from PGMI_PARAM_*") {
		t.Fatalf("expected verbose environment output, got:\n%s", output)
	}
	if strings.Contains(output, "SENTINEL_ENV") {
		t.Errorf("verbose output must not contain the value:\n%s", output)
	}
}

func TestLoadParamsFromFiles_Stdin(t *testing.T) {
	mfs := fakefs.NewMemoryFileSystem("/")
	mfs.AddFile("/test/a.env", "k=file\nother=file")

	got, err := loadParamsFromFiles(mfs, strings.NewReader("k=stdin\n"), []string{"/test/a.env", "-"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"k": "stdin", "other": "file"}; !maps.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestLoadParamsFromFiles_StdinRefusals(t *testing.T) {
	mfs := fakefs.NewMemoryFileSystem("/")
	for _, tc := range []struct {
		name  string
		stdin *strings.Reader
		files []string
	}{
		{"twice", strings.NewReader("k=v\n"), []string{"-", "-"}},
		{"no stdin", nil, []string{"-"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var err error
			if tc.stdin == nil {
				_, err = loadParamsFromFiles(mfs, nil, tc.files, false)
			} else {
				_, err = loadParamsFromFiles(mfs, tc.stdin, tc.files, false)
			}
			if !errors.Is(err, pgmi.ErrInvalidConfig) {
				t.Fatalf("want ErrInvalidConfig (exit 10), got %v", err)
			}
		})
	}
}

func TestServeDeployReadsParamsFromEnvironmentButNotStdin(t *testing.T) {
	clearPGEnv(t)
	t.Setenv("PGMI_PARAM_REGION", "eu")
	args := mcpDeployArgs{
		Path:       t.TempDir(),
		Connection: "postgresql://u@db.example/postgres",
		Database:   "app",
	}

	cfg, _, err := mcpDeploymentConfig(args)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Parameters["region"] != "eu" {
		t.Errorf("parameters = %v, want region=eu from PGMI_PARAM_REGION", cfg.Parameters)
	}

	args.ParamsFile = stringList{"-"}
	_, _, err = mcpDeploymentConfig(args)
	if got := pgmi.ExitCodeForError(err); got != pgmi.ExitConfigError {
		t.Errorf("paramsFile \"-\": exit %d, want %d: %v", got, pgmi.ExitConfigError, err)
	}
}
