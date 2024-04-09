-- name: CreateIndexedData :exec
INSERT INTO indexed_data (public_key, from_value, to_value, role, signers, signature, round, root, height)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (public_key, from_value, to_value, role, height, round) DO UPDATE SET
			signers = EXCLUDED.signers,
			signature = EXCLUDED.signature,
			root = EXCLUDED.root;