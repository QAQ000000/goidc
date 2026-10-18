package store

import (
	"context"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/supplier"
)

// AppConfigurationCount contains only aggregate state. No instance config,
// credential bytes, account URLs or user names are read into application memory.
type AppConfigurationCount struct {
	Source                string
	Category              channel.Category
	ImplementationID      string
	SchemaVersion         int
	InstanceCount         int64
	EnabledInstanceCount  int64
	SecretConfiguredCount int64
}

func (s *Store) AppConfigurationCounts(ctx context.Context) ([]AppConfigurationCount, error) {
	// Both configuration stores share one statement snapshot. Grouping by the
	// persisted schema version exposes incompatibility without loading config or
	// decrypting credentials. Legacy provision channel rows remain visible but
	// are not equivalent to live supplier accounts.
	rows, err := s.Pool.Query(ctx, `
SELECT 'channel',category,implementation_id,schema_version,count(*),
 count(*) FILTER (WHERE enabled),count(*) FILTER (WHERE octet_length(secret_ciphertext)>0 OR secret_fields<>'{}'::jsonb)
FROM channel_instances GROUP BY category,implementation_id,schema_version
UNION ALL
SELECT 'supplier','provision',provider,schema_version,count(*),
 count(*) FILTER (WHERE active),count(*) FILTER (WHERE (credential_format=0 AND octet_length(credentials_encrypted)>0) OR (credential_format=1 AND secure_fields<>'{}'::jsonb))
FROM supplier_accounts GROUP BY provider,schema_version`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	providers := make(map[string]string)
	for _, definition := range supplier.Definitions() {
		providers[definition.Provider.ID] = definition.Descriptor.ID
	}
	result := make([]AppConfigurationCount, 0)
	for rows.Next() {
		var item AppConfigurationCount
		if err = rows.Scan(&item.Source, &item.Category, &item.ImplementationID, &item.SchemaVersion, &item.InstanceCount, &item.EnabledInstanceCount, &item.SecretConfiguredCount); err != nil {
			return nil, err
		}
		if item.Source == "supplier" {
			id, compiled := providers[item.ImplementationID]
			if compiled {
				item.ImplementationID = id
			}
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
