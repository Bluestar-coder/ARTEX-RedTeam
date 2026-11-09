package recon

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestCTBackendBench is the reproducible M2 CT backend comparison. It is skipped
// unless CT_BENCH=1; run it in the manual verification workflow and capture the
// CTBENCH lines. Defaults: seeds example.com,python.org; 5 runs each.
func TestCTBackendBench(t *testing.T) {
	if os.Getenv("CT_BENCH") != "1" {
		t.Skip("set CT_BENCH=1 to run the CT backend benchmark")
	}
	seeds := splitCSV(os.Getenv("CT_BENCH_SEEDS"))
	if len(seeds) == 0 {
		seeds = []string{"example.com", "python.org"}
	}
	runs := 5
	if v := os.Getenv("CT_BENCH_RUNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			runs = n
		}
	}
	cfg := ConfigFromEnv()
	cfg.CTBackends = []string{"crt.sh", "crt.name", "certspotter"}
	backends := CTBackendDefs(cfg, nil)
	if len(backends) != 3 {
		t.Fatalf("backends=%d", len(backends))
	}
	ctx := context.Background()
	for _, b := range backends {
		for _, seed := range seeds {
			for run := 1; run <= runs; run++ {
				start := time.Now()
				names, err := b.Lookup(ctx, seed)
				dur := time.Since(start)
				ok := err == nil
				errMsg := ""
				if err != nil {
					errMsg = strings.ReplaceAll(err.Error(), ",", ";")
				}
				// One machine-parsable line per query.
				t.Logf("CTBENCH backend=%s seed=%s run=%d ok=%v dur_ms=%d names=%d err=%s",
					b.Name(), seed, run, ok, dur.Milliseconds(), len(names), errMsg)
				fmt.Printf("CTBENCH,%s,%s,%d,%v,%d,%d,%s\n", b.Name(), seed, run, ok, dur.Milliseconds(), len(names), errMsg)
			}
		}
	}
}
