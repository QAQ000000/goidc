package store

import (
	"context"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/supplier"
)

// AppConfigurationCount contains only aggregate state. No instance config,
// credential bytes, account URLs or user names are read into application memory.
type AppConfigurationCount struct {
	Category              channel.Category
	ImplementationID      string
	InstanceCount         int64
	EnabledInstanceCount  int64
	SecretConfiguredCount int64
}

func (s *Store) AppConfigurationCounts(ctx context.Context) ([]AppConfigurationCount, error) {
	// Both configuration stores share one statement snapshot. Provision channel
	// rows and supplier accounts are distinct instances and contribute together.
	rows, err := s.Pool.Query(ctx, `
SELECT 'channel',category,implementation_id,count(*),
 count(*) FILTER (WHERE enabled),count(*) FILTER (WHERE octet_length(secret_ciphertext)>0)
FROM channel_instances GROUP BY category,implementation_id
UNION ALL
SELECT 'supplier','provision',provider,count(*),
 count(*) FILTER (WHERE active),count(*) FILTER (WHERE octet_length(credentials_encrypted)>0)
FROM supplier_accounts GROUP BY provider`)
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
		var source string
		var item AppConfigurationCount
		if err = rows.Scan(&source, &item.Category, &item.ImplementationID, &item.InstanceCount, &item.EnabledInstanceCount, &item.SecretConfiguredCount); err != nil {
			return nil, err
		}
		if source == "supplier" {
			id, compiled := providers[item.ImplementationID]
			if !compiled {
				continue
			}
			item.ImplementationID = id
		}
		result = append(result, item)
	}
	return result, rows.Err()
}
