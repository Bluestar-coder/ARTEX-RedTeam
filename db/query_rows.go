package db

import (
	"database/sql"
)

// RowScanner maps the current row of rows into a value of T.
type RowScanner[T any] func(*sql.Rows) (T, error)

// sqlQueryer is implemented by *DB (embedded *sql.DB) and *sql.Tx, so the same
// helper works for autocommit queries and transaction-scoped queries.
type sqlQueryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// scanRows is the single owner of the rows.Next/Scan/append/rows.Err contract.
// Callers keep their SQL text and pass the query error in unchanged; on scan or
// query error the original error is returned. Zero rows yield a nil slice —
// callers whose historical contract was a non-nil empty slice must normalize
// afterwards (`if out == nil { out = []T{} }`).
func scanRows[T any](rows *sql.Rows, queryErr error, scan RowScanner[T]) ([]T, error) {
	if queryErr != nil {
		return nil, queryErr
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// queryRows is the entry point for new SELECT methods (including the upcoming
// recon-phase asset queries): the SQL is passed through verbatim, every row is
// mapped with scan, and Close/rows.Err semantics match the hand-written loops it
// replaces. Example:
//
//	rows, err := queryRows(d, `
//	SELECT id, domain FROM assets WHERE company_id=$1 ORDER BY id`, []any{companyID},
//		func(r *sql.Rows) (Asset, error) {
//			var a Asset
//			err := r.Scan(&a.ID, &a.Domain)
//			return a, err
//		})
func queryRows[T any](q sqlQueryer, query string, args []any, scan RowScanner[T]) ([]T, error) {
	rows, err := q.Query(query, args...)
	return scanRows(rows, err, scan)
}
