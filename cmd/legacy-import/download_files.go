package main

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/legacy"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func (im *importer) snapshotEntities() []string {
	keys := make([]string, 0, len(im.source))
	for key := range im.source {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}
func downloadRef(r legacy.Record, key string) (int64, error) {
	n, err := strconv.ParseInt(r[key], 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid file reference: %d/%s", sourceID(r), key)
	}
	return n, nil
}
func (im *importer) validateDownloadFiles() error {
	for _, e := range []string{"download_categories", "downloads", "download_products", "customer_files"} {
		if _, ok := im.source[e]; !ok {
			return fmt.Errorf("download snapshot required: %s", e)
		}
	}
	for _, entity := range []string{"download_categories", "downloads", "download_products", "customer_files"} {
		for _, r := range im.source[entity] {
			fields := []string{"create_time"}
			if entity != "customer_files" {
				fields = append(fields, "update_time")
			}
			for _, field := range fields {
				n, err := downloadRef(r, field)
				if err != nil || n > 253402300799 {
					return fmt.Errorf("invalid download timestamp: %s/%d/%s", entity, sourceID(r), field)
				}
			}
		}
	}
	cats := index(im.source["download_categories"])
	files := index(im.source["downloads"])
	users := index(im.source["users"])
	products := index(im.source["products"])
	for _, r := range im.source["download_categories"] {
		id := sourceID(r)
		parent, err := downloadRef(r, "parentid")
		if err != nil {
			return err
		}
		if parent != 0 && cats[parent] == nil {
			return fmt.Errorf("missing download parent: %d", id)
		}
		name := strings.TrimSpace(r["name"])
		if name == "" || !legacy.DownloadTextValid(name, 255, false) || !legacy.DownloadTextValid(r["description"], 1000, true) || (r["hidden"] != "0" && r["hidden"] != "1") {
			return fmt.Errorf("invalid download category: %d", id)
		}
		n, err := strconv.ParseInt(r["sort"], 10, 64)
		if err != nil || n < 0 || n > 1000000 {
			return fmt.Errorf("invalid download category sort: %d", id)
		}
		seen := map[int64]bool{id: true}
		for parent > 0 {
			if seen[parent] {
				return fmt.Errorf("download category cycle: %d", id)
			}
			seen[parent] = true
			parent, err = downloadRef(cats[parent], "parentid")
			if err != nil {
				return err
			}
		}
	}
	pairs := map[[2]int64]bool{}
	bindings := map[int64]int{}
	for _, r := range im.source["download_products"] {
		p, err := downloadRef(r, "product_id")
		if err != nil {
			return err
		}
		f, err := downloadRef(r, "download_id")
		if err != nil {
			return err
		}
		if p == 0 || f == 0 || products[p] == nil || files[f] == nil {
			return fmt.Errorf("missing download product relationship: %d", sourceID(r))
		}
		pair := [2]int64{p, f}
		if pairs[pair] {
			return fmt.Errorf("duplicate download product relationship: %d", sourceID(r))
		}
		pairs[pair] = true
		bindings[f]++
	}
	for _, r := range im.source["downloads"] {
		id := sourceID(r)
		cat, err := downloadRef(r, "category")
		if err != nil {
			return err
		}
		if cat != 0 && cats[cat] == nil {
			return fmt.Errorf("missing download category: %d", id)
		}
		if strings.TrimSpace(r["title"]) == "" || !legacy.DownloadTextValid(r["title"], 255, false) || !legacy.DownloadTextValid(r["description"], 10000, true) {
			return fmt.Errorf("invalid download text: %d", id)
		}
		for _, key := range []string{"hidden", "clientsonly", "productdownload"} {
			if r[key] != "0" && r[key] != "1" {
				return fmt.Errorf("invalid download flag: %d/%s", id, key)
			}
		}
		if _, err = downloadRef(r, "downloads"); err != nil {
			return err
		}
		if r["productdownload"] == "1" && bindings[id] == 0 {
			return fmt.Errorf("product-restricted download without products: %d", id)
		}
		if r["filetype"] == "remote" {
			u, err := url.Parse(r["locationname"])
			if err != nil || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || strings.ContainsAny(r["locationname"], "\\ \r\n\t") || !legacy.DownloadTextValid(r["locationname"], 2048, false) {
				return fmt.Errorf("invalid legacy external link: %d", id)
			}
		} else if r["filetype"] != "upload" && r["filetype"] != "manual" && r["filetype"] != "" {
			return fmt.Errorf("unsupported legacy file source: %d", id)
		}
	}
	for _, r := range im.source["customer_files"] {
		uid, err := downloadRef(r, "uid")
		if err != nil {
			return err
		}
		if uid == 0 || users[uid] == nil {
			return fmt.Errorf("missing customer file owner: %d", sourceID(r))
		}
		if strings.TrimSpace(r["name"]) == "" || !legacy.DownloadTextValid(r["name"], 255, false) || !legacy.DownloadTextValid(r["remarks"], 10000, true) {
			return fmt.Errorf("invalid customer file text: %d", sourceID(r))
		}
		if _, err = downloadRef(r, "adminid"); err != nil {
			return err
		}
	}
	return nil
}

func fileDisplayName(r legacy.Record, private bool) string {
	stored, name := r["location"], r["locationname"]
	if private {
		stored, name = r["url"], r["downame"]
	}
	if name != "" {
		return name
	}
	if _, suffix, ok := strings.Cut(stored, "^"); ok {
		return suffix
	}
	return stored
}
func (im *importer) readSourceFile(r legacy.Record, private bool) ([]byte, string, error) {
	stored, uid := r["location"], int64(0)
	if private {
		stored = r["url"]
		uid = referenceID(r, "uid")
	}
	b, mt, err := legacy.ReadDownloadFile(im.uploadRoot, uid, stored, fileDisplayName(r, private))
	if err != nil {
		return nil, "", fmt.Errorf("legacy file %d: %s", sourceID(r), err)
	}
	return b, mt, nil
}

// Fingerprints become part of the immutable source archive. Bytes are read one
// file at a time again on insertion and checked against this preflight hash.
func (im *importer) prepareDownloadFiles() error {
	for _, entity := range []string{"downloads", "customer_files"} {
		for _, r := range im.source[entity] {
			if entity == "downloads" && r["filetype"] == "remote" {
				continue
			}
			b, mt, err := im.readSourceFile(r, entity == "customer_files")
			if err != nil {
				return err
			}
			r["_file_sha256"] = checksum(b)
			r["_file_size"] = strconv.Itoa(len(b))
			r["_file_mime"] = mt
			r["_file_name"] = fileDisplayName(r, entity == "customer_files")
		}
	}
	return nil
}
func (im *importer) checkFileAuthor(ctx context.Context, tx pgx.Tx) error {
	if len(im.source["downloads"])+len(im.source["customer_files"])+len(im.source["staff"])+len(im.source["tickets"]) == 0 {
		return nil
	}
	var valid bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND role='super_admin' AND status='active')", im.fileAuthorID).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return fmt.Errorf("active target super administrator --file-author-id required")
	}
	return nil
}
func (im *importer) fileTargetIsNew(ctx context.Context, tx pgx.Tx, entity string, r legacy.Record) error {
	var archived bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM legacy_source_records WHERE source=$1 AND entity=$2 AND legacy_id=$3)", im.sourceName, entity, sourceID(r)).Scan(&archived); err != nil {
		return err
	}
	if archived {
		return fmt.Errorf("missing archived file target: %s/%d; restore or review required", entity, sourceID(r))
	}
	return nil
}
func (im *importer) importDownloadFiles(ctx context.Context, tx pgx.Tx, users, products map[int64]int64, runID int64) error {
	cats := map[int64]int64{}
	files := map[int64]int64{}
	links := map[int64]int64{}
	private := map[int64]int64{}
	remaining := slices.Clone(im.source["download_categories"])
	for len(remaining) > 0 {
		next := []legacy.Record{}
		for _, r := range remaining {
			parent := referenceID(r, "parentid")
			if parent > 0 && cats[parent] == 0 {
				next = append(next, r)
				continue
			}
			replay, err := im.replay(ctx, tx, "download_categories", r, cats)
			if err != nil {
				return err
			}
			if replay {
				continue
			}
			if err = im.fileTargetIsNew(ctx, tx, "download_categories", r); err != nil {
				return err
			}
			var parentID *int64
			if parent > 0 {
				p := cats[parent]
				parentID = &p
			}
			var id int64
			if err = tx.QueryRow(ctx, `INSERT INTO download_categories(name,description,sort_order,active,parent_id,created_at,updated_at,legacy_source,legacy_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id`, strings.TrimSpace(r["name"]), r["description"], referenceID(r, "sort"), r["hidden"] == "0", parentID, ts(r["create_time"]), sourceUpdated(r), im.sourceName, sourceID(r)).Scan(&id); err != nil {
				return err
			}
			cats[sourceID(r)] = id
			im.report.Mapped["download_categories"]++
			if err = im.recordMapped(ctx, tx, "download_categories", r, id, runID); err != nil {
				return err
			}
		}
		if len(next) == len(remaining) {
			return fmt.Errorf("unresolved category hierarchy")
		}
		remaining = next
	}
	for _, r := range im.source["downloads"] {
		replay, err := im.replay(ctx, tx, "downloads", r, files)
		if err != nil {
			return err
		}
		if replay {
			continue
		}
		if err = im.fileTargetIsNew(ctx, tx, "downloads", r); err != nil {
			return err
		}
		var category *int64
		if c := cats[referenceID(r, "category")]; c > 0 {
			category = &c
		}
		access := "public"
		if r["clientsonly"] == "1" {
			access = "login"
		}
		if r["productdownload"] == "1" {
			access = "product"
		}
		source, link, name, mt, hash := "local", "", "", "", ""
		var b []byte
		if r["filetype"] == "remote" {
			source = "remote"
			link = r["locationname"]
		} else {
			b, mt, err = im.readSourceFile(r, false)
			if err != nil {
				return err
			}
			hash = checksum(b)
			if hash != r["_file_sha256"] {
				return fmt.Errorf("legacy bytes changed after preflight: downloads/%d", sourceID(r))
			}
			name = fileDisplayName(r, false)
		}
		var id int64
		if err = tx.QueryRow(ctx, `INSERT INTO downloads(category_id,title,description,active,access_level,source_type,remote_url,filename,mime_type,size_bytes,sha256,content,author_id,downloads,created_at,updated_at,legacy_source,legacy_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18) RETURNING id`, category, strings.TrimSpace(r["title"]), r["description"], r["hidden"] == "0", access, source, link, name, mt, len(b), hash, b, im.fileAuthorID, referenceID(r, "downloads"), ts(r["create_time"]), sourceUpdated(r), im.sourceName, sourceID(r)).Scan(&id); err != nil {
			return err
		}
		files[sourceID(r)] = id
		im.report.Mapped["downloads"]++
		if err = im.recordMapped(ctx, tx, "downloads", r, id, runID); err != nil {
			return err
		}
		if err = store.Audit(ctx, tx, im.fileAuthorID, "download.imported", "download", id, map[string]any{"source": im.sourceName, "legacy_id": sourceID(r), "run_id": runID}); err != nil {
			return err
		}
	}
	for _, r := range im.source["download_products"] {
		replay, err := im.replay(ctx, tx, "download_products", r, links)
		if err != nil {
			return err
		}
		if replay {
			continue
		}
		// Removed live bindings must never be recreated by replay. Require review.
		var archived bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM legacy_source_records WHERE source=$1 AND entity='download_products' AND legacy_id=$2)", im.sourceName, sourceID(r)).Scan(&archived); err != nil {
			return err
		}
		if archived {
			return fmt.Errorf("missing archived file product relationship: %d; restore or review required", sourceID(r))
		}
		var id int64
		if err = tx.QueryRow(ctx, `INSERT INTO download_products(download_id,product_id,legacy_source,legacy_id) VALUES($1,$2,$3,$4) RETURNING id`, files[referenceID(r, "download_id")], products[referenceID(r, "product_id")], im.sourceName, sourceID(r)).Scan(&id); err != nil {
			return err
		}
		im.report.Mapped["download_products"]++
		if err = im.recordMapped(ctx, tx, "download_products", r, id, runID); err != nil {
			return err
		}
	}
	for _, r := range im.source["customer_files"] {
		replay, err := im.replay(ctx, tx, "customer_files", r, private)
		if err != nil {
			return err
		}
		if replay {
			continue
		}
		if err = im.fileTargetIsNew(ctx, tx, "customer_files", r); err != nil {
			return err
		}
		b, mt, err := im.readSourceFile(r, true)
		if err != nil {
			return err
		}
		hash := checksum(b)
		if hash != r["_file_sha256"] {
			return fmt.Errorf("legacy bytes changed after preflight: customer_files/%d", sourceID(r))
		}
		var id int64
		if err = tx.QueryRow(ctx, `INSERT INTO customer_files(customer_id,title,remarks,filename,mime_type,size_bytes,sha256,content,uploader_id,created_at,updated_at,legacy_source,legacy_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10,$11,$12) RETURNING id`, users[referenceID(r, "uid")], strings.TrimSpace(r["name"]), r["remarks"], fileDisplayName(r, true), mt, len(b), hash, b, im.fileAuthorID, ts(r["create_time"]), im.sourceName, sourceID(r)).Scan(&id); err != nil {
			return err
		}
		im.report.Mapped["customer_files"]++
		if err = im.recordMapped(ctx, tx, "customer_files", r, id, runID); err != nil {
			return err
		}
		if err = store.Audit(ctx, tx, im.fileAuthorID, "customer_file.imported", "customer_file", id, map[string]any{"source": im.sourceName, "legacy_id": sourceID(r), "legacy_admin_id": referenceID(r, "adminid"), "run_id": runID}); err != nil {
			return err
		}
	}
	return nil
}
func sourceUpdated(r legacy.Record) time.Time {
	if referenceID(r, "update_time") == 0 {
		return ts(r["create_time"])
	}
	return ts(r["update_time"])
}
