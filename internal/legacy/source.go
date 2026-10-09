package legacy

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Record uses strings for exact MySQL DECIMAL and integer timestamp preservation.
// Only explicit, reviewed columns may enter the snapshot (no SELECT *).
type Record map[string]string
type Snapshot map[string][]Record

var queries = []struct{ entity, query string }{
	{"currencies", "SELECT id,code FROM shd_currencies ORDER BY id"},
	{"customer_groups", "SELECT id,group_name,group_colour,discount_percent,susptermexempt,separateinvoices FROM shd_client_groups ORDER BY id"},
	{"users", "SELECT id,username,email,password,status,currency,credit,groupid,create_time,update_time FROM shd_clients ORDER BY id"},
	{"contacts", "SELECT id,uid,username,sex,avatar,companyname,email,wechat_id,country,province,city,region,address1,address2,postcode,phonenumber,permissions,generalemails,invoiceemails,productemails,supportemails,status,create_time,update_time FROM shd_contacts ORDER BY id"},
	{"products", "SELECT id,name,description,type,hidden,retired,stock_control,qty,pay_type,groupid,create_time,update_time FROM shd_products ORDER BY id"},
	{"product_config_groups", "SELECT id,name,description,upstream_id,global FROM shd_product_config_groups ORDER BY id"},
	{"product_config_links", "SELECT id,gid,pid FROM shd_product_config_links ORDER BY id"},
	{"product_config_options", "SELECT id,gid,option_name,option_type,qty_minimum,qty_maximum,`order`,hidden,upgrade,upstream_id,auto,is_discount,notes,is_rebate,qty_stage,unit,senior,linkage_pid,linkage_top_pid,linkage_level,copy_id FROM shd_product_config_options ORDER BY id"},
	{"product_config_option_subs", "SELECT id,config_id,qty_minimum,qty_maximum,option_name,sort_order,hidden,upstream_id,linkage_pid,linkage_top_pid,linkage_level,copy_id FROM shd_product_config_options_sub ORDER BY id"},
	{"product_config_option_links", "SELECT id,config_id,sub_id,relation,type,relation_id,upstream_id FROM shd_product_config_options_links ORDER BY id"},
	{"product_config_prices", "SELECT id,relid,currency,osetupfee,hsetupfee,dsetupfee,msetupfee,qsetupfee,ssetupfee,asetupfee,bsetupfee,tsetupfee,onetime,hour,day,monthly,quarterly,semiannually,annually,biennially,triennially FROM shd_pricing WHERE type='configoptions' ORDER BY id"},
	{"service_option_selections", "SELECT id,relid,configid,optionid,qty FROM shd_host_config_options ORDER BY id"},
	{"service_navigation_groups", "SELECT id,groupname,fa_icon,`order` FROM shd_nav_group ORDER BY id"},
	{"service_navigation_preferences", "SELECT id,uid,groupid,is_show FROM shd_nav_group_user ORDER BY id"},
	{"prices", "SELECT id,relid,currency,onetime,monthly,quarterly,semiannually,annually,biennially,triennially,osetupfee,msetupfee,qsetupfee,ssetupfee,asetupfee,bsetupfee,tsetupfee FROM shd_pricing WHERE type='product' ORDER BY id"},
	{"orders", "SELECT id,uid,ordernum,status,amount,invoiceid,create_time,update_time,delete_time FROM shd_orders ORDER BY id"},
	{"invoices", "SELECT id,uid,total,subtotal,credit,tax,tax2,status,type,create_time,due_time,paid_time,delete_time,is_delete FROM shd_invoices ORDER BY id"},
	{"invoice_items", "SELECT id,invoice_id,uid,type,rel_id,description,amount,due_time,delete_time FROM shd_invoice_items ORDER BY id"},
	{"services", "SELECT id,uid,orderid,productid,domain,domainstatus,billingcycle,firstpaymentamount,amount,nextduedate,create_time,update_time,dcimid FROM shd_host ORDER BY id"},
	{"download_categories", "SELECT id,parentid,name,description,hidden,sort,create_time,update_time FROM shd_downloadcats ORDER BY id"},
	{"downloads", "SELECT id,category,type,title,description,downloads,location,locationname,filetype,clientsonly,hidden,productdownload,create_time,update_time FROM shd_downloads ORDER BY id"},
	{"download_products", "SELECT id,product_id,download_id,create_time,update_time FROM shd_product_downloads ORDER BY id"},
	{"customer_files", "SELECT id,uid,name,remarks,url,downame,adminid,create_time FROM shd_userdownloads ORDER BY id"},
	{"email_templates", "SELECT id,type,name,subject,message,disabled,language,create_time,update_time FROM shd_email_templates ORDER BY id"},
	{"staff", "SELECT id,user_login,user_nickname,user_status,create_time FROM shd_user WHERE user_type=1 ORDER BY id"},
	{"ticket_departments", "SELECT id,name,description,only_reg_client,only_client_open,no_auto_reply,hidden,`order`,is_product_order,is_related_upstream,is_certifi FROM shd_ticket_department ORDER BY id"},
	{"ticket_statuses", "SELECT id,title,`order`,show_active,show_await,auto_close FROM shd_ticket_status ORDER BY id"},
	{"tickets", "SELECT id,tid,dptid,uid,host_id,create_time,title,content,status,priority,admin,admin_id,attachment,last_reply_time,client_unread,admin_unread,merged_ticket_id,update_time,handle,handle_time,is_deliver,is_receive,upstream_tid FROM shd_ticket ORDER BY id"},
	{"ticket_replies", "SELECT id,tid,uid,contactid,create_time,content,admin,admin_id,attachment,star,editor,is_deliver,is_receive,source FROM shd_ticket_reply ORDER BY id"},
	{"ticket_notes", "SELECT id,tid,admin,create_time,content,attachment,editor FROM shd_ticket_note ORDER BY id"},
}

func Read(ctx context.Context, db *sql.DB) (Snapshot, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	out := Snapshot{}
	for _, q := range queries {
		rows, err := tx.QueryContext(ctx, q.query)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", q.entity, err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return nil, err
		}
		out[q.entity] = []Record{}
		for rows.Next() {
			values := make([]sql.NullString, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = rows.Scan(pointers...); err != nil {
				rows.Close()
				return nil, err
			}
			record := Record{}
			for i, name := range columns {
				if values[i].Valid {
					record[name] = values[i].String
				}
			}
			out[q.entity] = append(out[q.entity], record)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	if err := ExpandTicketAttachments(out); err != nil {
		return nil, err
	}
	return out, tx.Commit()
}

// Cents rejects precision loss, exponents and overflow instead of rounding.
func Cents(raw string) (int64, error) {
	if raw == "" {
		return 0, fmt.Errorf("empty monetary value")
	}
	negative := strings.HasPrefix(raw, "-")
	digits := raw
	if negative {
		digits = digits[1:]
	}
	parts := strings.Split(digits, ".")
	if len(parts) > 2 || len(parts[0]) == 0 {
		return 0, fmt.Errorf("invalid monetary value")
	}
	fractional := "00"
	if len(parts) == 2 {
		if len(parts[1]) < 1 || len(parts[1]) > 2 {
			return 0, fmt.Errorf("unsupported monetary precision")
		}
		fractional = parts[1] + strings.Repeat("0", 2-len(parts[1]))
	}
	for _, c := range parts[0] + fractional {
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("invalid monetary digits")
		}
	}
	cents, err := strconv.ParseInt(parts[0]+fractional, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("monetary overflow")
	}
	if negative {
		cents = -cents
	}
	return cents, nil
}
func number(r Record, key string) int64 { n, _ := strconv.ParseInt(r[key], 10, 64); return n }
func instant(r Record, key string) *time.Time {
	n := number(r, key)
	if n <= 0 {
		return nil
	}
	t := time.Unix(n, 0).UTC()
	return &t
}
