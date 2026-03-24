-- Add language column to campaigns table.
ALTER TABLE mgmt.campaigns ADD COLUMN IF NOT EXISTS language TEXT NOT NULL DEFAULT '';
