package infra

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"accounts/pkg/datasource/objectstore"
)

// retainedObjectManifests is how many listings a source keeps: the one its
// cursor names, the one a sync in flight just listed, and slack for a sync that
// listed and then failed before the cursor moved. A version past it resyncs.
const retainedObjectManifests = 4

// PutDatasourceObjectManifest retains one listing of an object-storage source
// under its version, inside the source's org transaction, and prunes all but
// the newest retained listings. A version is the listing's own digest, so a
// second put of it only marks it newest.
func (s *PostgresStore) PutDatasourceObjectManifest(ctx context.Context, org, source, version string, objects []objectstore.Item) error {
	if objects == nil {
		objects = []objectstore.Item{}
	}
	encoded, err := json.Marshal(objects)
	if err != nil {
		return err
	}
	q := s.getQueryExecutor(ctx)
	if _, err := q.Exec(ctx, `INSERT INTO datasource_object_manifests (org_id,source_id,version,objects,retained_at) VALUES ($1,$2,$3,$4,clock_timestamp())
  ON CONFLICT (org_id,source_id,version) DO UPDATE SET retained_at = clock_timestamp()`, org, source, version, encoded); err != nil {
		return err
	}
	_, err = q.Exec(ctx, `DELETE FROM datasource_object_manifests WHERE org_id=$1 AND source_id=$2 AND version NOT IN (
  SELECT version FROM datasource_object_manifests WHERE org_id=$1 AND source_id=$2 ORDER BY retained_at DESC, version LIMIT $3)`,
		org, source, retainedObjectManifests)
	return err
}

// GetDatasourceObjectManifest returns the listing a source retained under
// version, and false when it never retained one or has pruned it.
func (s *PostgresStore) GetDatasourceObjectManifest(ctx context.Context, org, source, version string) ([]objectstore.Item, bool, error) {
	var encoded []byte
	err := s.getQueryExecutor(ctx).QueryRow(ctx, `SELECT objects FROM datasource_object_manifests WHERE org_id=$1 AND source_id=$2 AND version=$3`,
		org, source, version).Scan(&encoded)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var objects []objectstore.Item
	if err := json.Unmarshal(encoded, &objects); err != nil {
		return nil, false, err
	}
	return objects, true, nil
}
