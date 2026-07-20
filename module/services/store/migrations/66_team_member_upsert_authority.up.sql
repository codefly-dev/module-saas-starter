-- AddTeamMember uses INSERT ... ON CONFLICT DO UPDATE. PostgreSQL requires
-- UPDATE authority for the statement even when the insert path wins.
GRANT UPDATE ON team_members TO app_tenant;
