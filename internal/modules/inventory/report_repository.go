package inventory

import (
	"context"
	"strings"
)

// ReportNodes is an internal read contract for authorized registry reports.
// Callers pass only scopes checked in the same transaction. Each type is queried
// in batches; actual foreign-key ownership determines its scope, never URL IDs.
func (r *Repository) ReportNodes(ctx context.Context, ids []string) ([]Node, error) {
	return r.reportNodesFor(ctx, ids, []Kind{Dormitory, Building, Floor, Room, Bed})
}

// ReportDormitories supplies scope labels without loading descendants or history.
func (r *Repository) ReportDormitories(ctx context.Context, ids []string) ([]Node, error) {
	return r.reportNodesFor(ctx, ids, []Kind{Dormitory})
}
func (r *Repository) reportNodesFor(ctx context.Context, ids []string, kinds []Kind) ([]Node, error) {
	var nodes []Node
	for _, kind := range kinds {
		schema, err := schemaFor(kind)
		if err != nil {
			return nil, err
		}
		for start := 0; start < len(ids); start += 400 {
			part := ids[start:min(start+400, len(ids))]
			args := make([]any, len(part))
			for i, id := range part {
				args[i] = id
			}
			rows, err := r.db.QueryContext(ctx, selectSQL(schema)+" WHERE "+schema.root+" IN ("+strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")+") ORDER BY n.id", args...)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				node, err := scanNode(rows, kind)
				if err != nil {
					rows.Close()
					return nil, err
				}
				nodes = append(nodes, node)
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
		}
	}
	return nodes, nil
}

type ReportPhotoOwner struct {
	Kind Kind
	ID   string
}

func (r *Repository) ReportPhotoCounts(ctx context.Context, ids []string) (map[ReportPhotoOwner]int64, error) {
	counts := map[ReportPhotoOwner]int64{}
	for start := 0; start < len(ids); start += 400 {
		part := ids[start:min(start+400, len(ids))]
		args := make([]any, len(part))
		for i, id := range part {
			args[i] = id
		}
		slots := strings.TrimSuffix(strings.Repeat("?,", len(part)), ",")
		for _, q := range []struct {
			kind Kind
			sql  string
		}{
			{Dormitory, `SELECT p.dormitory_id,count(*) FROM inventory_photos p WHERE p.dormitory_id IN (` + slots + `) GROUP BY p.dormitory_id`},
			{Room, `SELECT p.room_id,count(*) FROM inventory_photos p JOIN inventory_rooms r ON r.id=p.room_id JOIN inventory_floors f ON f.id=r.floor_id JOIN inventory_buildings b ON b.id=f.building_id WHERE b.dormitory_id IN (` + slots + `) GROUP BY p.room_id`},
		} {
			rows, err := r.db.QueryContext(ctx, q.sql, args...)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var id string
				var count int64
				if err = rows.Scan(&id, &count); err != nil {
					rows.Close()
					return nil, err
				}
				counts[ReportPhotoOwner{Kind: q.kind, ID: id}] = count
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return nil, err
			}
		}
	}
	return counts, nil
}
