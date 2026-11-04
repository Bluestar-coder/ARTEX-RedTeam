package evidence

import (
	"os"
	"testing"

	"github.com/Bluestar-coder/ARTEX-RedTeam/internal/testdb"
)

// TestMain holds the shared PostgreSQL suite lock for the whole package run.
// Every DB handle must be opened through testdb.Open so the schema migration
// runs under that lock; see internal/testdb for why.
func TestMain(m *testing.M) { os.Exit(testdb.Lock(m)) }
