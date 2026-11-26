package share

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

func LegacyConversation(platform, value, declared string) (string, string, error) {
	kind, id, ok := strings.Cut(value, ":")
	switch kind {
	case "supergroup":
		kind = "group"
	case "c2c":
		kind = "private"
	}
	if declared == "supergroup" {
		declared = "group"
	}
	if declared == "c2c" {
		declared = "private"
	}
	if ok && id != "" && (kind == "private" || kind == "group" || kind == "channel" || kind == "cron" || kind == "elnis") {
		if declared != "" && declared != kind {
			return "", "", fmt.Errorf("conversation kind conflict: %q and %q", value, declared)
		}
		return kind, id, nil
	}
	if platform == "cli" && (declared == "" || declared == "cli") {
		return "private", value, nil
	}
	return "", "", fmt.Errorf("ambiguous conversation: platform=%q scope=%q kind=%q", platform, value, declared)
}

// Tables with conversation keys are rebuilt before values are converted, so equal
// raw IDs in different conversation kinds cannot conflict with the old unique key.
func MigrateConversationTable(ctx context.Context, tx *sql.Tx, table string) error {
	rows, err := tx.QueryContext(ctx, "SELECT type,sql FROM sqlite_master WHERE tbl_name=? AND sql IS NOT NULL ORDER BY type", table)
	if err != nil {
		return err
	}
	type object struct{ kind, sql string }
	var objects []object
	for rows.Next() {
		var o object
		if err = rows.Scan(&o.kind, &o.sql); err != nil {
			rows.Close()
			return err
		}
		objects = append(objects, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var definition string
	for _, o := range objects {
		if o.kind == "table" {
			definition = o.sql
		}
	}

	var sequence int64
	if table == "chat_messages" {
		if err = tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq),0) FROM sqlite_sequence WHERE name=?", table).Scan(&sequence); err != nil {
			return err
		}
	}
	oldColumn := "platform_scope_id"
	if table == "media_outputs" || table == "media_history" {
		oldColumn = "scope_id"
	}
	convertDDL := func(sql string) string {
		sql = regexp.MustCompile(`\bplatform,\s*`+oldColumn).ReplaceAllString(sql, "platform, chat_type, chat_id")
		sql = strings.ReplaceAll(sql, oldColumn, "chat_id")
		if table == "chat_messages" {
			return strings.ReplaceAll(sql, "scope_type", "chat_type")
		}
		return strings.ReplaceAll(sql, "chat_id TEXT NOT NULL", "chat_type TEXT NOT NULL, chat_id TEXT NOT NULL")
	}
	temp := "migration_" + table
	definition = convertDDL(definition)
	definition = strings.Replace(definition, table, temp, 1)
	if _, err = tx.ExecContext(ctx, definition); err != nil {
		return err
	}
	if err = MigrateRows(ctx, tx, table, func(row map[string]any) error {
		declared, _ := row["scope_type"].(string)
		kind, id, err := LegacyConversation(row["platform"].(string), row[oldColumn].(string), declared)
		if err != nil {
			return err
		}
		delete(row, oldColumn)
		delete(row, "scope_type")
		delete(row, "_rowid")
		row["chat_type"], row["chat_id"] = kind, id
		columns := make([]string, 0, len(row))
		for column := range row {
			columns = append(columns, column)
		}
		sort.Strings(columns)
		args := make([]any, 0, len(columns))
		for _, column := range columns {
			args = append(args, row[column])
		}
		_, err = tx.ExecContext(ctx, "INSERT INTO "+temp+" ("+strings.Join(columns, ",")+") VALUES ("+strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",")+")", args...)
		return err
	}); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DROP TABLE "+table+"; ALTER TABLE "+temp+" RENAME TO "+table); err != nil {
		return err
	}

	if table == "chat_messages" {
		if _, err = tx.ExecContext(ctx, "DELETE FROM sqlite_sequence WHERE name=?", table); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO sqlite_sequence(name,seq) VALUES(?,?)", table, sequence); err != nil {
			return err
		}
	}
	for _, o := range objects {
		if o.kind != "table" {
			if _, err = tx.ExecContext(ctx, convertDDL(o.sql)); err != nil {
				return err
			}
		}
	}
	return nil
}

func MigrateRows(ctx context.Context, tx *sql.Tx, table string, convert func(map[string]any) error) error {
	rows, err := tx.QueryContext(ctx, "SELECT rowid AS _rowid,* FROM "+table)
	if err != nil {
		return err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return err
	}
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err = rows.Scan(dest...); err != nil {
			return err
		}
		row := make(map[string]any, len(columns))
		for i, column := range columns {
			row[column] = values[i]
		}
		id := row["_rowid"]
		if err = convert(row); err != nil {
			return fmt.Errorf("%s row %v: %w", table, id, err)
		}
	}
	return rows.Err()
}
