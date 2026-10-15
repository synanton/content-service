package jobsboot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAllBinariesWireAuditor guards the mechanical auditor wiring: the
// helper is nil-tolerant, so a main that forgets Auditor fails silently
// (job runs, upstream failures occur, no audit record). This test reads
// the entrypoints and fails if any job or API wiring drops the auditor —
// the same silent-omission class as a stranded follow-up commit, caught
// at test time instead of evidence-review time.
func TestAllBinariesWireAuditor(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		main     string
		mustHold []string
	}{
		{"cmd/disposition-job/main.go", []string{"Auditor: env.Auditor"}},
		{"cmd/orphan-sweeper/main.go", []string{"Auditor: env.Auditor"}},
		{"cmd/parquet-compactor/main.go", []string{"Auditor: env.Auditor"}},
		{"cmd/integrity-job/main.go", []string{"Auditor: env.Auditor"}},
		{"cmd/content-api/main.go", []string{
			"security.NewAuditor(",
			"NewHandler(store, auditor)",
			"NewServer(store, auditor)",
		}},
	}
	for _, tc := range cases {
		raw, err := os.ReadFile(filepath.Join(root, tc.main))
		if err != nil {
			t.Fatalf("%s: %v", tc.main, err)
		}
		// gofmt aligns struct field colons; compact whitespace so the
		// guard checks wiring presence, not formatting.
		compacted := strings.ReplaceAll(strings.ReplaceAll(string(raw), " ", ""), "\t", "")
		for _, want := range tc.mustHold {
			compactWant := strings.ReplaceAll(strings.ReplaceAll(want, " ", ""), "\t", "")
			if !strings.Contains(compacted, compactWant) {
				t.Errorf("%s: auditor wiring %q missing", tc.main, want)
			}
		}
	}
}
