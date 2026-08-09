-- contacts.custom_fields is intentionally untouched: it predates this migration
-- (000003) and holds user data that has no other home. Dropping definitions
-- makes the values unlabelled, not invalid.
DROP TABLE IF EXISTS contact_field_defs;
DROP TYPE IF EXISTS contact_field_type;
