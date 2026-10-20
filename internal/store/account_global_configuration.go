package store

import "context"

func (s *Store) MutateAccountGlobalConfiguration(ctx context.Context, actor, id int64, operation, key string, in GlobalConfigurationInput) (map[string]any, error) {
	credential, ok := ctx.Value(storageCredentialKey{}).(storageCredential)
	if !ok || credential.APIKey || credential.Hash == "" {
		return nil, conflict("unauthorized", "请先登录")
	}
	tx, err := s.CatalogTx(ctx, actor)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = RequireAdministratorPermission(ctx, tx, actor, "products.view"); err != nil {
		return nil, err
	}
	if err = RequireAdministratorPermission(ctx, tx, actor, "products.manage"); err != nil {
		return nil, err
	}
	if err = LockFileCredential(ctx, tx, actor); err != nil {
		return nil, err
	}
	hash := accountBusinessHash(struct {
		ID    int64
		Input GlobalConfigurationInput
	}{id, in})
	op := "global_configuration." + operation
	result, replayed, err := loadAccountBusinessReplay(ctx, tx, actor, op, key, hash)
	if err != nil {
		return nil, err
	}
	if replayed {
		return result, tx.Commit(ctx)
	}
	result, err = MutateGlobalConfiguration(ctx, tx, actor, id, operation, in)
	if err != nil {
		return nil, err
	}
	if err = SaveOperation(ctx, tx, actor, "admin:"+op, key, hash, result); err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}
