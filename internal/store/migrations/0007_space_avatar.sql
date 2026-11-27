-- Space custom avatar support: stores content-addressed SHA-256 and verified raster media type.
ALTER TABLE spaces ADD COLUMN avatar_sha256 TEXT NOT NULL DEFAULT '';
ALTER TABLE spaces ADD COLUMN avatar_media_type TEXT NOT NULL DEFAULT '';
