ALTER TABLE bots ADD COLUMN type TEXT NOT NULL DEFAULT 'personal';
ALTER TABLE bots ADD CONSTRAINT bots_type_check CHECK (type IN ('personal', 'public'));
