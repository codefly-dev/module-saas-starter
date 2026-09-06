-- Restore the free-text target_collection from the bound collection node's label
-- and drop the boundary binding. The collection scope nodes minted by the up
-- migration are left in place: they are indistinguishable from product-registered
-- 'collection' nodes, and deleting them could orphan grants placed against them.
DROP INDEX IF EXISTS idx_datasource_sources_boundary;

ALTER TABLE datasource_sources
    ADD COLUMN target_collection TEXT;

UPDATE datasource_sources ds
    SET target_collection = sn.label
    FROM scope_nodes sn
    WHERE sn.id = ds.boundary_node_id;

ALTER TABLE datasource_sources
    ALTER COLUMN target_collection SET NOT NULL,
    DROP COLUMN boundary_node_id;
