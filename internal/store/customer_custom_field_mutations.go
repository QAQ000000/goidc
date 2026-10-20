package store

import (
	"context"
	"github.com/jackc/pgx/v5"
	"strconv"
)

type CustomerCustomFieldInput struct {
	FieldKey          string   `json:"field_key"`
	Label             string   `json:"label"`
	FieldType         string   `json:"field_type"`
	Description       string   `json:"description"`
	ValidationPattern string   `json:"validation_pattern"`
	Choices           []string `json:"choices"`
	SortOrder         int      `json:"sort_order"`
	AdminOnly         bool     `json:"admin_only"`
	Required          bool     `json:"required"`
	ShowOnRegister    bool     `json:"show_on_register"`
	Active            bool     `json:"active"`
	ExpectedVersion   int64    `json:"expected_version"`
}

func (s *Store) CreateCustomerCustomField(ctx context.Context, actor int64, in CustomerCustomFieldInput, key string) (map[string]any, error) {
	hash := accountBusinessHash(in)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, _, _, err = lockCustomerAccountManager(ctx, tx, actor); err != nil {
		return nil, err
	}
	operation := "customer_custom_field.create"
	if err = lockCustomerAccountMutation(ctx, tx, actor, operation, key); err != nil {
		return nil, err
	}
	if replay, replayed, e := loadAccountBusinessReplay(ctx, tx, actor, operation, key, hash); e != nil {
		return nil, e
	} else if replayed {

		if e = commitCustomerAccountMutation(ctx, tx, actor); e != nil {
			return nil, e
		}
		return replay, nil
	}
	// Definition writers serialize on the same catalogue before any field row,
	// matching the statement trigger and profile validation catalogue lock.
	var catalogueVersion int64
	if err = tx.QueryRow(ctx, "SELECT version FROM customer_field_catalogue WHERE id=1 FOR UPDATE").Scan(&catalogueVersion); err != nil {
		return nil, err
	}
	if err = recheckCustomerAccountCredential(ctx, tx, actor); err != nil {
		return nil, err
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO customer_custom_fields(field_key,label,field_type,description,validation_pattern,choices,sort_order,admin_only,required,show_on_register,active) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING id`, in.FieldKey, in.Label, in.FieldType, in.Description, in.ValidationPattern, in.Choices, in.SortOrder, in.AdminOnly, in.Required, in.ShowOnRegister, in.Active).Scan(&id)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"id": id, "version": 1}
	if err = Audit(ctx, tx, actor, "customer_custom_field.created", "customer_custom_field", id, map[string]any{"field_key": in.FieldKey}); err != nil {
		return nil, err
	}
	if err = saveCustomerAccountOperation(ctx, tx, actor, operation, key, hash, result); err != nil {
		return nil, err
	}

	if err = commitCustomerAccountMutation(ctx, tx, actor); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) UpdateCustomerCustomField(ctx context.Context, actor int64, id int64, in CustomerCustomFieldInput, key string) (map[string]any, error) {
	hash := accountBusinessHash(struct {
		ID    int64
		Input CustomerCustomFieldInput
	}{id, in})
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, _, _, err = lockCustomerAccountManager(ctx, tx, actor); err != nil {
		return nil, err
	}
	operation := "customer_custom_field.update:" + strconv.FormatInt(id, 10)
	if err = lockCustomerAccountMutation(ctx, tx, actor, operation, key); err != nil {
		return nil, err
	}
	if replay, replayed, e := loadAccountBusinessReplay(ctx, tx, actor, operation, key, hash); e != nil {
		return nil, e
	} else if replayed {

		if e = commitCustomerAccountMutation(ctx, tx, actor); e != nil {
			return nil, e
		}
		return replay, nil
	}
	// Definition writers serialize on the same catalogue before any field row,
	// matching the statement trigger and profile validation catalogue lock.
	var catalogueVersion int64
	if err = tx.QueryRow(ctx, "SELECT version FROM customer_field_catalogue WHERE id=1 FOR UPDATE").Scan(&catalogueVersion); err != nil {
		return nil, err
	}
	if err = recheckCustomerAccountCredential(ctx, tx, actor); err != nil {
		return nil, err
	}
	var version int64
	var fieldKey string
	if err = tx.QueryRow(ctx, "SELECT version,field_key FROM customer_custom_fields WHERE id=$1 FOR UPDATE", id).Scan(&version, &fieldKey); err != nil {
		if err == pgx.ErrNoRows {
			return nil, &AccountBusinessError{404, "not_found", "客户自定义字段不存在"}
		}
		return nil, err
	}
	if fieldKey != in.FieldKey {
		return nil, &AccountBusinessError{409, "field_key_immutable", "字段标识创建后不可修改"}
	}
	if version != in.ExpectedVersion {
		return nil, &AccountBusinessError{409, "version_conflict", "客户自定义字段已变更，请刷新后重试"}
	}
	if _, err = UpdateVersioned(ctx, tx, "customer_custom_fields", `field_key=$1,label=$2,field_type=$3,description=$4,validation_pattern=$5,choices=$6,sort_order=$7,admin_only=$8,required=$9,show_on_register=$10,active=$11`, id, in.ExpectedVersion, in.FieldKey, in.Label, in.FieldType, in.Description, in.ValidationPattern, in.Choices, in.SortOrder, in.AdminOnly, in.Required, in.ShowOnRegister, in.Active); err != nil {
		return nil, err
	}
	result := map[string]any{"id": id, "version": version + 1}
	if err = Audit(ctx, tx, actor, "customer_custom_field.updated", "customer_custom_field", id, map[string]any{"expected_version": in.ExpectedVersion}); err != nil {
		return nil, err
	}
	if err = saveCustomerAccountOperation(ctx, tx, actor, operation, key, hash, result); err != nil {
		return nil, err
	}

	if err = commitCustomerAccountMutation(ctx, tx, actor); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) DeleteCustomerCustomField(ctx context.Context, actor int64, id, expectedVersion int64, key string) (map[string]any, error) {
	hash := accountBusinessHash(struct {
		ID              int64
		ExpectedVersion int64
	}{id, expectedVersion})
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, _, _, err = lockCustomerAccountManager(ctx, tx, actor); err != nil {
		return nil, err
	}
	operation := "customer_custom_field.delete:" + strconv.FormatInt(id, 10)
	if err = lockCustomerAccountMutation(ctx, tx, actor, operation, key); err != nil {
		return nil, err
	}
	if replay, replayed, e := loadAccountBusinessReplay(ctx, tx, actor, operation, key, hash); e != nil {
		return nil, e
	} else if replayed {

		if e = commitCustomerAccountMutation(ctx, tx, actor); e != nil {
			return nil, e
		}
		return replay, nil
	}
	// Definition writers serialize on the same catalogue before any field row,
	// matching the statement trigger and profile validation catalogue lock.
	var catalogueVersion int64
	if err = tx.QueryRow(ctx, "SELECT version FROM customer_field_catalogue WHERE id=1 FOR UPDATE").Scan(&catalogueVersion); err != nil {
		return nil, err
	}
	if err = recheckCustomerAccountCredential(ctx, tx, actor); err != nil {
		return nil, err
	}
	var version int64
	if err = tx.QueryRow(ctx, "SELECT version FROM customer_custom_fields WHERE id=$1 FOR UPDATE", id).Scan(&version); err != nil {
		if err == pgx.ErrNoRows {
			return nil, &AccountBusinessError{404, "not_found", "客户自定义字段不存在"}
		}
		return nil, err
	}
	if version != expectedVersion {
		return nil, &AccountBusinessError{409, "version_conflict", "客户自定义字段已变更，请刷新后重试"}
	}
	if _, err = tx.Exec(ctx, "DELETE FROM customer_custom_fields WHERE id=$1", id); err != nil {
		return nil, err
	}
	result := map[string]any{"id": id, "deleted": true}
	if err = Audit(ctx, tx, actor, "customer_custom_field.deleted", "customer_custom_field", id, map[string]any{"expected_version": expectedVersion}); err != nil {
		return nil, err
	}
	if err = saveCustomerAccountOperation(ctx, tx, actor, operation, key, hash, result); err != nil {
		return nil, err
	}

	if err = commitCustomerAccountMutation(ctx, tx, actor); err != nil {
		return nil, err
	}
	return result, nil
}
