package store

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
)

type NativeRulePredicate struct {
	GroupID    int64  `json:"group_id"`
	OptionID   *int64 `json:"option_id"`
	Operator   string `json:"operator"`
	QtyMinimum *int64 `json:"qty_minimum"`
	QtyMaximum *int64 `json:"qty_maximum"`
}

type NativeConfigurationRuleInput struct {
	Name             string                `json:"name"`
	Enabled          *bool                 `json:"enabled"`
	Conditions       []NativeRulePredicate `json:"conditions"`
	Results          []NativeRulePredicate `json:"results"`
	ExpectedRevision *int64                `json:"expected_revision"`
}

func validateNativeRuleInput(in *NativeConfigurationRuleInput) error {
	in.Name = strings.TrimSpace(in.Name)
	if !contentTextValid(in.Name, 120, false) || in.Name == "" || in.Enabled == nil || len(in.Conditions) < 1 || len(in.Conditions) > 20 || len(in.Results) < 1 || len(in.Results) > 20 {
		return conflict("invalid_configuration_rule", "规则名称或条件数量无效")
	}
	for _, p := range append(append([]NativeRulePredicate{}, in.Conditions...), in.Results...) {
		if p.GroupID < 1 || p.GroupID > 1<<53-1 {
			return conflict("invalid_configuration_rule", "规则配置组编号无效")
		}
		switch p.Operator {
		case "absent":
			if p.OptionID != nil || p.QtyMinimum != nil || p.QtyMaximum != nil {
				return conflict("invalid_configuration_rule", "未选择条件不能指定选项或数量")
			}
		case "equals", "not_equals":
			if p.OptionID == nil || *p.OptionID < 1 || *p.OptionID > 1<<53-1 || p.QtyMinimum != nil || p.QtyMaximum != nil {
				return conflict("invalid_configuration_rule", "选项比较条件无效")
			}
		case "quantity_in", "quantity_out":
			if p.OptionID == nil || *p.OptionID < 1 || *p.OptionID > 1<<53-1 || p.QtyMinimum == nil || p.QtyMaximum == nil || *p.QtyMinimum < 1 || *p.QtyMaximum < *p.QtyMinimum || *p.QtyMaximum > 1000000 {
				return conflict("invalid_configuration_rule", "数量范围条件无效")
			}
		default:
			return conflict("invalid_configuration_rule", "规则运算符无效")
		}
	}
	return nil
}

func validateNativeRuleReferences(ctx context.Context, tx pgx.Tx, product int64, in NativeConfigurationRuleInput) error {
	for _, p := range append(append([]NativeRulePredicate{}, in.Conditions...), in.Results...) {
		var optionType int64
		var active bool
		err := tx.QueryRow(ctx, `SELECT g.option_type,g.active AND ($3::bigint IS NULL OR coalesce(o.active,false))
FROM product_option_groups g LEFT JOIN product_options o ON o.id=$3 AND o.group_id=g.id
WHERE g.id=$1 AND g.product_id=$2 AND ($3::bigint IS NULL OR o.id IS NOT NULL)`, p.GroupID, product, p.OptionID).Scan(&optionType, &active)
		if err == pgx.ErrNoRows {
			return conflict("invalid_configuration_rule", "规则引用了其他商品、停用配置或非数量选项")
		}
		if err != nil {
			return err
		}
		if !active || ((p.Operator == "quantity_in" || p.Operator == "quantity_out") && !isQuantityOptionType(optionType)) {
			return conflict("invalid_configuration_rule", "规则引用了其他商品、停用配置或非数量选项")
		}
	}
	return nil
}

func (s *Store) SaveNativeConfigurationRule(ctx context.Context, actor, product, rule int64, in NativeConfigurationRuleInput) (int64, error) {
	if err := validateNativeRuleInput(&in); err != nil {
		return 0, err
	}
	tx, err := s.configurationTx(ctx, actor, product, in.ExpectedRevision, true)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err = validateNativeRuleReferences(ctx, tx, product, in); err != nil {
		return 0, err
	}
	action := "product.configuration_rule.updated"
	if rule == 0 {
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM native_configuration_rules WHERE product_id=$1", product).Scan(&count); err != nil {
			return 0, err
		}
		if count >= 100 {
			return 0, conflict("configuration_limit", "每个商品最多 100 条规则")
		}
		action = "product.configuration_rule.created"
		err = tx.QueryRow(ctx, "INSERT INTO native_configuration_rules(product_id,name,enabled) VALUES($1,$2,$3) RETURNING id", product, in.Name, *in.Enabled).Scan(&rule)
	} else {
		var existing int64
		if err = tx.QueryRow(ctx, "SELECT id FROM native_configuration_rules WHERE id=$1 AND product_id=$2 FOR UPDATE", rule, product).Scan(&existing); err != nil {
			return 0, err
		}
		_, err = tx.Exec(ctx, "UPDATE native_configuration_rules SET name=$2,enabled=$3,updated_at=now() WHERE id=$1", rule, in.Name, *in.Enabled)
		if err == nil {
			_, err = tx.Exec(ctx, "DELETE FROM native_configuration_predicates WHERE rule_id=$1", rule)
		}
	}
	if err != nil {
		return 0, err
	}
	for _, phase := range []struct {
		name       string
		predicates []NativeRulePredicate
	}{{"condition", in.Conditions}, {"result", in.Results}} {
		for i, p := range phase.predicates {
			if _, err = tx.Exec(ctx, `INSERT INTO native_configuration_predicates(rule_id,phase,sort_order,group_id,option_id,operator,qty_minimum,qty_maximum) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, rule, phase.name, i, p.GroupID, p.OptionID, p.Operator, p.QtyMinimum, p.QtyMaximum); err != nil {
				return 0, err
			}
		}
	}
	return rule, configurationCommit(ctx, tx, actor, product, rule, action)
}

func (s *Store) DeleteNativeConfigurationRule(ctx context.Context, actor, product, rule int64, revision *int64) error {
	tx, err := s.configurationTx(ctx, actor, product, revision, true)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, "DELETE FROM native_configuration_rules WHERE id=$1 AND product_id=$2", rule, product)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return pgx.ErrNoRows
	}
	return configurationCommit(ctx, tx, actor, product, rule, "product.configuration_rule.deleted")
}

func nativePredicateMatches(p NativeRulePredicate, selected map[int64]OptionSnapshot) bool {
	choice, present := selected[p.GroupID]
	switch p.Operator {
	case "absent":
		return !present
	case "equals":
		return present && p.OptionID != nil && choice.ID == *p.OptionID
	case "not_equals":
		return present && p.OptionID != nil && choice.ID != *p.OptionID
	case "quantity_in", "quantity_out":
		if !present || p.OptionID == nil || choice.ID != *p.OptionID || p.QtyMinimum == nil || p.QtyMaximum == nil {
			return false
		}
		inside := choice.Quantity >= *p.QtyMinimum && choice.Quantity <= *p.QtyMaximum
		return inside == (p.Operator == "quantity_in")
	}
	return false
}

func validateNativeConfigurationRules(ctx context.Context, q sqlQueryer, product int64, selection OptionSelection) error {
	rows, err := q.Query(ctx, `SELECT r.id,r.name,p.phase,p.group_id,p.option_id,p.operator,p.qty_minimum,p.qty_maximum,g.active,g.option_type,o.active
FROM native_configuration_rules r LEFT JOIN native_configuration_predicates p ON p.rule_id=r.id
LEFT JOIN product_option_groups g ON g.id=p.group_id AND g.product_id=r.product_id
LEFT JOIN product_options o ON o.id=p.option_id AND o.group_id=p.group_id
WHERE r.product_id=$1 AND r.enabled ORDER BY r.id,p.phase,p.sort_order`, product)
	if err != nil {
		return err
	}
	defer rows.Close()
	type ruleState struct {
		name                string
		conditions, results []NativeRulePredicate
	}
	rules := map[int64]*ruleState{}
	order := []int64{}
	for rows.Next() {
		var id int64
		var name string
		var phase *string
		var p NativeRulePredicate
		var groupID, optionType *int64
		var operator *string
		var active, optionActive *bool
		if err = rows.Scan(&id, &name, &phase, &groupID, &p.OptionID, &operator, &p.QtyMinimum, &p.QtyMaximum, &active, &optionType, &optionActive); err != nil {
			return err
		}
		rule := rules[id]
		if rule == nil {
			rule = &ruleState{name: name}
			rules[id] = rule
			order = append(order, id)
		}
		if phase == nil || groupID == nil || operator == nil {
			return conflict("invalid_configuration_rule", "商品规则数据不完整")
		}
		p.GroupID, p.Operator = *groupID, *operator
		if active == nil || !*active || (p.OptionID != nil && (optionActive == nil || !*optionActive)) || ((p.Operator == "quantity_in" || p.Operator == "quantity_out") && (optionType == nil || !isQuantityOptionType(*optionType))) {
			return conflict("invalid_configuration_rule", "规则引用了其他商品、停用配置或非数量选项")
		}
		if *phase == "condition" {
			rule.conditions = append(rule.conditions, p)
		} else {
			rule.results = append(rule.results, p)
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	selected := map[int64]OptionSnapshot{}
	for _, snapshot := range selection.Snapshot {
		selected[snapshot.GroupID] = snapshot
	}
	for _, id := range order {
		rule := rules[id]
		if len(rule.conditions) == 0 || len(rule.results) == 0 {
			return conflict("invalid_configuration_rule", "商品规则数据不完整")
		}
		matches := true
		for _, p := range rule.conditions {
			if !nativePredicateMatches(p, selected) {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		for _, p := range rule.results {
			if !nativePredicateMatches(p, selected) {
				return conflict("configuration_dependency", "配置不符合规则："+rule.name)
			}
		}
	}
	return nil
}

func copyNativeConfigurationRules(ctx context.Context, tx pgx.Tx, source, target int64, groups, options map[int64]int64) error {
	rows, err := tx.Query(ctx, `SELECT r.id,r.name,r.enabled,p.phase,p.sort_order,p.group_id,p.option_id,p.operator,p.qty_minimum,p.qty_maximum
FROM native_configuration_rules r JOIN native_configuration_predicates p ON p.rule_id=r.id
WHERE r.product_id=$1 ORDER BY r.id,p.phase,p.sort_order`, source)
	if err != nil {
		return err
	}
	type predicateCopy struct {
		phase, operator  string
		sort             int
		group            int64
		option, min, max *int64
	}
	type ruleCopy struct {
		id         int64
		name       string
		enabled    bool
		predicates []predicateCopy
	}
	copies := []ruleCopy{}
	for rows.Next() {
		var id int64
		var name string
		var enabled bool
		var p predicateCopy
		if err = rows.Scan(&id, &name, &enabled, &p.phase, &p.sort, &p.group, &p.option, &p.operator, &p.min, &p.max); err != nil {
			rows.Close()
			return err
		}
		if len(copies) == 0 || copies[len(copies)-1].id != id {
			copies = append(copies, ruleCopy{id: id, name: name, enabled: enabled})
		}
		copies[len(copies)-1].predicates = append(copies[len(copies)-1].predicates, p)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	var ruleCount int
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM native_configuration_rules WHERE product_id=$1", source).Scan(&ruleCount); err != nil {
		return err
	}
	if ruleCount != len(copies) {
		return conflict("invalid_product_duplicate", "源商品规则数据不完整")
	}
	for _, copy := range copies {
		var newID int64
		if err = tx.QueryRow(ctx, "INSERT INTO native_configuration_rules(product_id,name,enabled) VALUES($1,$2,$3) RETURNING id", target, copy.name, copy.enabled).Scan(&newID); err != nil {
			return err
		}
		for _, p := range copy.predicates {
			newGroup, ok := groups[p.group]
			if !ok {
				return conflict("invalid_product_duplicate", "源商品规则配置组无效")
			}
			var newOption *int64
			if p.option != nil {
				mapped, exists := options[*p.option]
				if !exists {
					return conflict("invalid_product_duplicate", "源商品规则选项无效")
				}
				newOption = &mapped
			}
			if _, err = tx.Exec(ctx, `INSERT INTO native_configuration_predicates(rule_id,phase,sort_order,group_id,option_id,operator,qty_minimum,qty_maximum) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, newID, p.phase, p.sort, newGroup, newOption, p.operator, p.min, p.max); err != nil {
				return err
			}
		}
	}
	return nil
}
