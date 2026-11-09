package db

import (
	"encoding/json"
	"fmt"
	"time"
)

// ReconAsset is one standardized recon record ready for the asset graph. It is
// deliberately DB-shaped: internal/recon providers are DB-agnostic and the
// mapping layer (server/agent) converts Record -> ReconAsset.
type ReconAsset struct {
	Type       string // root_domain | subdomain | ip | service
	Value      string // domain, IP, or URL
	Parent     string // root domain for subdomains / host for IP & service
	Source     string // provider name, e.g. "active:ct" or "fofa"
	ObservedAt time.Time
	RawRef     string

	IP            string
	StatusCode    *int
	ContentLength *int64
	Title         string
	FaviconMMH3   string
	Technologies  []string
}

// SourceCount is one row of "which sources contributed assets".
type SourceCount struct {
	Source string `json:"source"`
	Count  int    `json:"count"`
}

// UpsertReconAssets is the single batch write entry for reconnaissance results.
// It reuses the existing per-type ON CONFLICT upserts (no hand-written INSERT
// loops), then stamps source/observed_at/raw_ref on each affected row. A repeat
// observation only updates last_seen/observed_at — it never adds a row.
func (s *AssetStore) UpsertReconAssets(taskID int64, records []ReconAsset) (int, error) {
	if len(records) == 0 {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck
	scoped := &AssetStore{db: s.db, company: s.company, tx: tx}

	written := 0
	for _, rec := range records {
		ids, err := scoped.upsertReconOne(taskID, rec)
		if err != nil {
			return written, fmt.Errorf("recon %s %q: %w", rec.Type, rec.Value, err)
		}
		for _, id := range ids {
			if err := scoped.stampReconMeta(id, rec); err != nil {
				return written, err
			}
		}
		written++
	}
	if err := tx.Commit(); err != nil {
		return written, err
	}
	return written, nil
}

func (s *AssetStore) upsertReconOne(taskID int64, rec ReconAsset) ([]int64, error) {
	switch rec.Type {
	case "root_domain":
		id, err := s.UpsertRootDomain(UpsertRootDomainReq{Domain: rec.Value, TaskID: taskID})
		return []int64{id}, err
	case "subdomain":
		id, err := s.UpsertSubdomain(UpsertSubdomainReq{Domain: rec.Value, TaskID: taskID})
		return []int64{id}, err
	case "ip":
		id, err := s.UpsertIP(UpsertIPReq{IP: rec.Value, BoundDomains: nonEmpty(rec.Parent), TaskID: taskID})
		return []int64{id}, err
	case "service":
		host, _, _ := parseURL(rec.Value)
		id, err := s.UpsertHTTPService(UpsertHTTPServiceReq{
			URL: rec.Value, Technologies: rec.Technologies, StatusCode: rec.StatusCode,
			ContentLength: rec.ContentLength, PageTitle: rec.Title, FaviconMMH3: rec.FaviconMMH3,
			IP: rec.IP, TaskID: taskID,
		})
		if err != nil {
			return nil, err
		}
		// The service host is also a subdomain asset (unless it is the root
		// domain itself, which root_domain already covers).
		if host != "" && host != rec.Parent {
			subID, err := s.UpsertSubdomain(UpsertSubdomainReq{Domain: host, TaskID: taskID})
			if err != nil {
				return nil, err
			}
			return []int64{id, subID}, nil
		}
		return []int64{id}, nil
	default:
		return nil, fmt.Errorf("unknown recon asset type %q", rec.Type)
	}
}

// stampReconMeta sets source/observed_at and merges raw_ref/parent into extra.
// raw_ref is provider-side evidence for M3; it is stored as extra JSON so the
// schema does not need a second provenance table in M1.
func (s *AssetStore) stampReconMeta(id int64, rec ReconAsset) error {
	if id <= 0 {
		return nil
	}
	var observed any
	if !rec.ObservedAt.IsZero() {
		observed = rec.ObservedAt.UTC()
	}
	extra := map[string]any{}
	if rec.RawRef != "" {
		extra["recon_raw_ref"] = rec.RawRef
	}
	if rec.Parent != "" {
		extra["recon_parent"] = rec.Parent
	}
	extraJSON, err := json.Marshal(extra)
	if err != nil {
		return err
	}
	_, err = s.tx.Exec(`
UPDATE assets SET
    source      = COALESCE(NULLIF($2,''), source),
    observed_at = COALESCE($3, observed_at, now()),
    last_seen   = GREATEST(last_seen, COALESCE($3, last_seen)),
    extra       = assets.extra || $4::jsonb
WHERE id = $1`, id, rec.Source, observed, extraJSON)
	return err
}

func nonEmpty(v string) []string {
	if v == "" {
		return nil
	}
	return []string{v}
}

// SourceCounts lists how many assets each recon source contributed.
func (s *AssetStore) SourceCounts() ([]SourceCount, error) {
	rows, err := s.db.Query(`SELECT source, count(*) FROM assets WHERE source <> '' GROUP BY source ORDER BY count(*) DESC, source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SourceCount
	for rows.Next() {
		var sc SourceCount
		if err := rows.Scan(&sc.Source, &sc.Count); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// CountBySource counts assets matching a recon source plus optional type /
// company / task filters (0 or "" = no filter).
func (s *AssetStore) CountBySource(source, typ string, companyID, taskID int64) (int, error) {
	var n int
	err := s.db.QueryRow(`
SELECT count(*) FROM assets
WHERE source = $1
  AND ($2 = '' OR type = $2)
  AND ($3 = 0 OR company_id = $3)
  AND ($4 = 0 OR $4 = ANY(task_ids))`, source, typ, companyID, taskID).Scan(&n)
	return n, err
}

// QueryBySource returns assets matching a recon source, newest first.
func (s *AssetStore) QueryBySource(source, typ string, companyID, taskID int64, limit, offset int) ([]*Asset, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.Query(assetSelectCols+`
WHERE source = $1
  AND ($2 = '' OR type = $2)
  AND ($3 = 0 OR company_id = $3)
  AND ($4 = 0 OR $4 = ANY(task_ids))
ORDER BY last_seen DESC, id DESC
LIMIT $5 OFFSET $6`, source, typ, companyID, taskID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAssets(rows)
}
