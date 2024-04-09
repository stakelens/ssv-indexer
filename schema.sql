-- Create the indexed_data table
CREATE TABLE IF NOT EXISTS indexed_data (
  id SERIAL PRIMARY KEY,
  public_key VARCHAR(255) NOT NULL,
  from_value INTEGER NOT NULL,
  to_value INTEGER NOT NULL,
  role VARCHAR(255) NOT NULL,
  signers INTEGER[] NOT NULL,
  signature VARCHAR(255) NOT NULL,
  round INTEGER NOT NULL,
  root INTEGER[] NOT NULL,
  height INTEGER NOT NULL
);

-- Create the unique index
CREATE UNIQUE INDEX IF NOT EXISTS idx_indexed_data_unique ON indexed_data (public_key, from_value, to_value, role, height, round);