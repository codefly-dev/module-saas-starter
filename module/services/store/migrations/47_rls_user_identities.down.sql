DROP POLICY IF EXISTS user_identities_user ON user_identities;
ALTER TABLE user_identities NO FORCE ROW LEVEL SECURITY;
ALTER TABLE user_identities DISABLE ROW LEVEL SECURITY;
