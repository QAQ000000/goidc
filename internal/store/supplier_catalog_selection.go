package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/codex/zjmfgo/internal/supplier"
	"github.com/jackc/pgx/v5"
)

// Only these non-secret limits are readable without decrypting the selection.
// The authenticated payload binds the provider IDs and customer input to its
// order item; it is never returned by service/customer projections.
type supplierServiceSelection struct {
	OrderItemID             int64  `json:"order_item_id"`
	Cycle                   string `json:"cycle"`
	MaximumCostCents        int64  `json:"maximum_cost_cents"`
	RenewalMaximumCostCents int64  `json:"renewal_maximum_cost_cents"`
	Encrypted               []byte `json:"encrypted"`
}

type supplierSelectedValues struct {
	BindingID int64            `json:"binding_id"`
	ProductID int64            `json:"product_id"`
	Options   map[int64]string `json:"options"`
	Fields    map[int64]string `json:"fields"`
}

type supplierCheckoutSelection struct {
	Snapshot supplierSnapshot
	Values   supplierSelectedValues
	Price    supplier.Price
	Cycle    string
}

func supplierCatalogConfiguration(ctx context.Context, q sqlQueryer, bindingID int64) (SupplierCatalogConfiguration, bool, error) {
	var raw []byte
	var configuration SupplierCatalogConfiguration
	err := q.QueryRow(ctx, "SELECT configuration FROM supplier_catalog_imports WHERE binding_id=$1", bindingID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return configuration, false, nil
	}
	if err != nil {
		return configuration, false, err
	}
	if json.Unmarshal(raw, &configuration) != nil || configuration.Product.ID < 1 || configuration.Product.Revision == "" || configuration.Options == nil || configuration.Fields == nil {
		return configuration, true, conflict("supplier_snapshot_invalid", "供应商商品配置快照无效")
	}
	return configuration, true, nil
}

func (s *Store) prepareSupplierCheckoutSelection(ctx context.Context, q sqlQueryer, item checkoutLine) (*supplierCheckoutSelection, error) {
	var bindingID int64
	err := q.QueryRow(ctx, "SELECT id FROM supplier_product_bindings WHERE product_id=$1", item.ProductID).Scan(&bindingID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	configuration, imported, err := supplierCatalogConfiguration(ctx, q, bindingID)
	if err != nil || !imported {
		return nil, err
	}
	b, err := s.supplierBindingWith(ctx, q, bindingID, false)
	if err != nil {
		return nil, err
	}
	if configuration.Product.ID != b.UpstreamProductID || configuration.Product.Revision != b.UpstreamRevision {
		return nil, conflict("supplier_snapshot_invalid", "供应商商品配置与绑定不匹配")
	}
	values := supplierSelectedValues{BindingID: b.ID, ProductID: b.ProductID, Options: map[int64]string{}, Fields: map[int64]string{}}
	for _, id := range item.OptionIDs {
		mapping, ok := configuration.Options[id]
		if !ok {
			return nil, conflict("supplier_snapshot_invalid", "所选规格没有上游映射")
		}
		var option *supplier.Option
		for i := range configuration.Product.Options {
			if configuration.Product.Options[i].ID == mapping.OptionID {
				option = &configuration.Product.Options[i]
				break
			}
		}
		if option == nil {
			return nil, conflict("supplier_snapshot_invalid", "上游规格映射无效")
		}
		if _, duplicate := values.Options[mapping.OptionID]; duplicate {
			return nil, conflict("invalid_product_options", "每个上游配置只能选择一项")
		}
		value := strconv.FormatInt(mapping.ChoiceID, 10)
		if option.Type == "quantity" {
			qty, ok := item.OptionQuantities[id]
			if !ok {
				return nil, conflict("invalid_product_option_quantity", "数量型配置必须提供数量")
			}
			value = strconv.FormatInt(qty, 10)
		}
		values.Options[mapping.OptionID] = value
	}
	for _, field := range item.PurchaseSnapshot {
		id, ok := configuration.Fields[field.Key]
		if !ok {
			return nil, conflict("supplier_snapshot_invalid", "购买字段没有上游映射")
		}
		values.Fields[id] = field.Value
	}
	price, err := supplier.SelectionPrice(configuration.Product, item.Cycle, values.Options, values.Fields)
	if err != nil {
		return nil, supplierReadError(err)
	}
	if price.AmountCents < 0 || price.SetupCents < 0 || price.AmountCents > 100000000-price.SetupCents {
		return nil, conflict("supplier_cost_limit", "所选上游规格金额超出允许范围")
	}
	return &supplierCheckoutSelection{Snapshot: supplierSnapshot{SupplierID: b.SupplierID, BindingID: b.ID, Provider: b.Provider}, Values: values, Price: price, Cycle: item.Cycle}, nil
}

func (s *Store) freezeSupplierCheckoutSelection(selected *supplierCheckoutSelection, orderItemID int64) ([]byte, error) {
	if selected == nil {
		return nil, nil
	}
	sealed, err := supplierSeal(s.ProvisionEncryptionKey, "order-selection", orderItemID, selected.Values)
	if err != nil {
		return nil, err
	}
	snap := selected.Snapshot
	snap.Selection = &supplierServiceSelection{OrderItemID: orderItemID, Cycle: selected.Cycle, MaximumCostCents: selected.Price.AmountCents + selected.Price.SetupCents, RenewalMaximumCostCents: selected.Price.AmountCents, Encrypted: sealed}
	return json.Marshal(snap)
}

// Reuse a caller's transaction connection and frozen order selection. Missing
// selections on configurable imports fail closed instead of buying defaults.
func (s *Store) applySupplierServiceSelection(ctx context.Context, q sqlQueryer, b *SupplierBinding, snap supplierSnapshot, orderItemID int64, cycle string, renewal bool) (map[int64]string, error) {
	invalid := func() (map[int64]string, error) {
		return nil, conflict("supplier_snapshot_invalid", "供应商服务选择快照无效")
	}
	if snap.BindingID != b.ID || snap.SupplierID != b.SupplierID || snap.Provider != b.Provider {
		return invalid()
	}
	configuration, imported, err := supplierCatalogConfiguration(ctx, q, b.ID)
	if err != nil {
		return nil, err
	}
	if !imported {
		if snap.Selection != nil || b.BillingCycle != cycle {
			return invalid()
		}
		var fields map[int64]string
		err = supplierUnseal(s.ProvisionEncryptionKey, "binding-fields", b.ProductID, b.sealed, &fields)
		return fields, err
	}
	selection := snap.Selection
	if selection == nil || selection.OrderItemID != orderItemID || orderItemID < 1 || selection.Cycle != cycle || configuration.Product.ID != b.UpstreamProductID || configuration.Product.Revision != b.UpstreamRevision {
		return invalid()
	}
	var values supplierSelectedValues
	if err = supplierUnseal(s.ProvisionEncryptionKey, "order-selection", orderItemID, selection.Encrypted, &values); err != nil {
		return nil, err
	}
	if values.BindingID != b.ID || values.ProductID != b.ProductID {
		return invalid()
	}
	price, err := supplier.SelectionPrice(configuration.Product, cycle, values.Options, values.Fields)
	if err != nil || price.AmountCents < 0 || price.SetupCents < 0 || price.AmountCents > 100000000-price.SetupCents || price.AmountCents+price.SetupCents != selection.MaximumCostCents || price.AmountCents != selection.RenewalMaximumCostCents {
		return invalid()
	}
	b.BillingCycle, b.ConfigOptions, b.MaximumCostCents = cycle, values.Options, selection.MaximumCostCents
	if renewal {
		b.MaximumCostCents = selection.RenewalMaximumCostCents
	}
	return values.Fields, nil
}
