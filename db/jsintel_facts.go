package db

import (
	"encoding/json"
	"fmt"
	"time"
)

// UpsertExtractedFact writes or merges a fact node keyed by payload["extract_key"].
// M2 extraction is repeatable: running the same JS scan twice must not create
// duplicate facts. When the key already exists the sources are unioned (all
// pages/JS files are kept) and the observed timestamp is refreshed.
//
// Fact payloads are plain JSONB (like every other exploration fact); no schema
// change is needed for M2.
func (s *ExplorationStore) UpsertExtractedFact(key string, payload map[string]any, anchors []int64) (id int64, created bool, err error) {
	if key == "" {
		return 0, false, fmt.Errorf("extract_key is required")
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["extract_key"] = key
	var oldRaw []byte
	err = s.db.QueryRow(`SELECT id, payload FROM exploration_nodes
WHERE exploration_id=$1 AND kind='fact' AND payload->>'extract_key'=$2
ORDER BY id LIMIT 1`, s.expID, key).Scan(&id, &oldRaw)
	if err == nil {
		merged := map[string]any{}
		_ = json.Unmarshal(oldRaw, &merged)
		mergeFactPayload(merged, payload)
		raw, mErr := json.Marshal(merged)
		if mErr != nil {
			return 0, false, mErr
		}
		if _, err := s.db.Exec(`UPDATE exploration_nodes SET payload=$2, state='confirmed' WHERE id=$1`, id, string(raw)); err != nil {
			return 0, false, err
		}
		if err := s.addFactAnchors(id, anchors); err != nil {
			return id, false, err
		}
		return id, false, nil
	}
	id, err = s.AddNode(KindFact, payload, 5, "confirmed", "jsintel", anchors)
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

func (s *ExplorationStore) addFactAnchors(id int64, anchors []int64) error {
	for _, a := range anchors {
		if a <= 0 {
			continue
		}
		if _, err := s.db.Exec(`INSERT INTO exploration_anchors(node_id, asset_id) VALUES ($1,$2) ON CONFLICT DO NOTHING`, id, a); err != nil {
			return err
		}
	}
	return nil
}

// mergeFactPayload unions sources and keeps the newest observed_at; scalar
// fields from the incoming payload win only when non-empty.
func mergeFactPayload(dst, src map[string]any) {
	for k, v := range src {
		switch k {
		case "sources":
			dst[k] = unionSources(dst[k], v)
		case "observed_at":
			if s, ok := v.(string); ok && s != "" {
				if old, _ := dst[k].(string); old == "" || s > old {
					dst[k] = s
				}
			}
		default:
			if v == nil {
				continue
			}
			if s, ok := v.(string); ok && s == "" {
				continue
			}
			dst[k] = v
		}
	}
}

func unionSources(a, b any) []any {
	seen := map[string]bool{}
	var out []any
	add := func(v any) {
		raw, err := json.Marshal(v)
		if err != nil {
			return
		}
		key := string(raw)
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, v)
	}
	each := func(v any) {
		switch x := v.(type) {
		case []any:
			for _, item := range x {
				add(item)
			}
		case nil:
		default:
			add(x)
		}
	}
	each(a)
	each(b)
	return out
}

// nowRFC3339 is used by callers building fact payloads.
func NowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }
