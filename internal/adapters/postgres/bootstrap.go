package postgresadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	schedulerdatabase "github.com/LordFoxFairy/kokoro-scheduler/database"
	"github.com/LordFoxFairy/kokoro-scheduler/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func ApplySchemaToEmptyDatabase(ctx context.Context, pool *pgxpool.Pool, target config.DatabaseTarget) error {
	if pool == nil || target.SchemaName() == "" {
		return errors.New("db:apply-schema requires a PostgreSQL pool and explicit owner namespace")
	}
	// Take a fresh snapshot after the owner lock, so a waiting installer sees
	// the first installation's committed objects.
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return errors.New("begin owner schema installation failed")
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanupCtx)
	}()
	var databaseName string
	if err := tx.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		return errors.New("inspect owner installation database failed")
	}
	schemaName := target.SchemaName()
	lockIdentity := "kokoro-scheduler:db:apply-schema:" + databaseName + ":" + schemaName
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockIdentity); err != nil {
		return errors.New("acquire owner schema installation lock failed")
	}
	// Namespace dependencies cover relations, routines, types and all other
	// schema-bound objects without another editable catalog object list.
	var occupied bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM pg_catalog.pg_depend d
		JOIN pg_catalog.pg_namespace n ON n.oid = d.refobjid
		WHERE d.refclassid = 'pg_catalog.pg_namespace'::regclass
		  AND n.nspname = $1)`, schemaName).Scan(&occupied); err != nil {
		return errors.New("inspect owner namespace objects failed")
	}
	if occupied {
		return errors.New("db:apply-schema requires an empty database schema; target owner namespace contains objects")
	}
	if _, err := tx.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+pgx.Identifier{schemaName}.Sanitize()); err != nil {
		return errors.New("create target owner namespace failed")
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('search_path', $1, true), set_config('timezone', 'UTC', true)`, schemaName); err != nil {
		return errors.New("select target owner installation session failed")
	}
	if _, err := tx.Exec(ctx, schedulerdatabase.Schema); err != nil {
		return errors.New("execute canonical database/schema.sql failed")
	}
	if err := checkNamespaceReady(ctx, tx, target); err != nil {
		return fmt.Errorf("verify owner installation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return errors.New("commit owner schema installation failed")
	}
	return nil
}

// SchemaCatalog is a logical DDL snapshot, not a business model or a physical
// PostgreSQL backup. Namespace identities owned by the target use SELF.
type SchemaCatalog struct {
	Tables []SchemaCatalogTable
	Types  []SchemaCatalogType
}

type SchemaCatalogIdentity struct {
	Namespace string
	Name      string
}

type SchemaCatalogTable struct {
	Identity           SchemaCatalogIdentity
	Kind               string
	Persistence        string
	AccessMethod       string
	Options            []string
	RowSecurity        bool
	ForceRowSecurity   bool
	ReplicaIdentity    string
	Comment            *string
	Columns            []SchemaCatalogColumn
	Constraints        []SchemaCatalogConstraint
	Indexes            []SchemaCatalogIndex
	ConstraintTriggers []SchemaCatalogConstraintTrigger
}

type SchemaCatalogColumn struct {
	Position     int16
	Name         string
	Type         *SchemaCatalogIdentity
	TypeModifier int32
	Dimensions   int16
	Collation    *SchemaCatalogIdentity
	NotNull      bool
	Identity     string
	Generated    string
	Dropped      bool
	Default      *string
	Storage      string
	Compression  string
	Comment      *string
}

type SchemaCatalogConstraint struct {
	Name           string
	Kind           string
	Columns        []string
	Validated      bool
	Deferrable     bool
	Deferred       bool
	NoInherit      bool
	IsLocal        bool
	InheritedCount int32
	Enforced       *bool
	Period         *bool
	Definition     string
	Expression     *string
	Index          *SchemaCatalogIdentity
	Comment        *string
}

type SchemaCatalogIndex struct {
	Identity         SchemaCatalogIdentity
	AccessMethod     string
	Options          []string
	Unique           bool
	Primary          bool
	Exclusion        bool
	Immediate        bool
	NullsNotDistinct bool
	Valid            bool
	Ready            bool
	Live             bool
	ReplicaIdentity  bool
	Clustered        bool
	Comment          *string
	KeyCount         int16
	AttributeCount   int16
	Columns          []SchemaCatalogIndexColumn
	Expressions      *string
	Predicate        *string
}

type SchemaCatalogIndexColumn struct {
	Position      int64
	Name          *string
	Definition    string
	Key           bool
	Options       int16
	OperatorClass *SchemaCatalogIdentity
	Collation     *SchemaCatalogIdentity
}

type SchemaCatalogType struct {
	Identity  SchemaCatalogIdentity
	Kind      string
	Category  string
	Defined   bool
	Length    int16
	ByValue   bool
	Alignment string
	Storage   string
	Delimiter string
	NotNull   bool
	Relation  *SchemaCatalogIdentity
	Element   *SchemaCatalogIdentity
	Array     *SchemaCatalogIdentity
	Base      *SchemaCatalogIdentity
	Comment   *string
}

// Internal UNIQUE recheck triggers have generated physical names. Their
// structural identity is the owning constraint, not the name containing an OID.
type SchemaCatalogConstraintTrigger struct {
	Constraint  string
	Function    SchemaCatalogIdentity
	TimingFlags int16
	Enabled     string
	Deferrable  bool
	Deferred    bool
	Columns     []string
	Arguments   string
	Index       *SchemaCatalogIdentity
}

// SchemaCatalogDifference deliberately contains paths only. Expressions and
// literal values stay in the snapshot and must not enter diagnostic messages.
type SchemaCatalogDifference struct {
	ObjectKind string
	ObjectName string
	Field      string
}

// ReadSchemaCatalog uses a single read-only snapshot and never installs,
// repairs or constructs a reference namespace. The caller owns the pool.
func ReadSchemaCatalog(ctx context.Context, pool *pgxpool.Pool, target config.DatabaseTarget) (SchemaCatalog, error) {
	if pool == nil || target.SchemaName() == "" {
		return SchemaCatalog{}, errors.New("read catalog requires a pool and explicit owner namespace")
	}
	readCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := pool.BeginTx(readCtx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return SchemaCatalog{}, schemaCatalogError(readCtx, "begin read-only catalog snapshot", err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_ = tx.Rollback(cleanupCtx)
	}()
	var exists bool
	if err := tx.QueryRow(readCtx, "SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname = $1)", target.SchemaName()).Scan(&exists); err != nil {
		return SchemaCatalog{}, schemaCatalogError(readCtx, "inspect catalog target", err)
	}
	if !exists {
		return SchemaCatalog{}, errors.New("catalog target namespace does not exist")
	}
	if _, err := tx.Exec(readCtx, "SELECT set_config('search_path', $1, true), set_config('timezone', 'UTC', true)", target.SchemaName()); err != nil {
		return SchemaCatalog{}, schemaCatalogError(readCtx, "select read-only catalog session", err)
	}
	var sessionReady bool
	if err := tx.QueryRow(readCtx, `SELECT current_schema() = $1
		AND current_setting('search_path') = $1 AND current_setting('TimeZone') = 'UTC'
		AND current_setting('transaction_isolation') = 'repeatable read'
		AND current_setting('transaction_read_only') = 'on'`, target.SchemaName()).Scan(&sessionReady); err != nil {
		return SchemaCatalog{}, schemaCatalogError(readCtx, "verify read-only catalog session", err)
	}
	if !sessionReady {
		return SchemaCatalog{}, errors.New("catalog session is not the explicit owner UTC read-only repeatable-read target")
	}
	var unsupported bool
	if err := tx.QueryRow(readCtx, schemaCatalogUnsupportedSQL, target.SchemaName()).Scan(&unsupported); err != nil {
		return SchemaCatalog{}, schemaCatalogError(readCtx, "inspect catalog object inventory", err)
	}
	if unsupported {
		return SchemaCatalog{}, errors.New("catalog contains an unsupported owner object or structure")
	}
	var snapshot SchemaCatalog
	if err := readSchemaCatalogJSON(readCtx, tx, schemaCatalogTablesSQL, target.SchemaName(), &snapshot.Tables); err != nil {
		return SchemaCatalog{}, err
	}
	if err := readSchemaCatalogJSON(readCtx, tx, schemaCatalogTypesSQL, target.SchemaName(), &snapshot.Types); err != nil {
		return SchemaCatalog{}, err
	}
	if !validSchemaCatalogIdentities(snapshot) {
		return SchemaCatalog{}, errors.New("catalog contains an unresolved structural identity")
	}
	if err := tx.Commit(readCtx); err != nil {
		return SchemaCatalog{}, schemaCatalogError(readCtx, "finish read-only catalog snapshot", err)
	}
	return snapshot, nil
}

func schemaCatalogError(ctx context.Context, operation string, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%s: %w", operation, ctx.Err())
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", operation, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, context.DeadlineExceeded)
	}
	// Driver errors can include SQL or literals. Preserve only the operation.
	return errors.New(operation + " failed")
}

func readSchemaCatalogJSON(ctx context.Context, tx pgx.Tx, query, schema string, result any) error {
	var encoded []byte
	if err := tx.QueryRow(ctx, query, schema).Scan(&encoded); err != nil {
		return schemaCatalogError(ctx, "read structural catalog metadata", err)
	}
	if err := json.Unmarshal(encoded, result); err != nil {
		return errors.New("decode structural catalog metadata failed")
	}
	return nil
}

func validSchemaCatalogIdentities(snapshot SchemaCatalog) bool {
	valid := func(id *SchemaCatalogIdentity) bool {
		return id == nil || (id.Namespace != "" && id.Name != "")
	}
	for _, table := range snapshot.Tables {
		if !valid(&table.Identity) || table.AccessMethod == "" {
			return false
		}
		for _, column := range table.Columns {
			if !valid(column.Type) || !valid(column.Collation) || (!column.Dropped && column.Type == nil) {
				return false
			}
		}
		for _, constraint := range table.Constraints {
			if !valid(constraint.Index) || constraint.Definition == "" {
				return false
			}
		}
		for _, index := range table.Indexes {
			if !valid(&index.Identity) || index.AccessMethod == "" {
				return false
			}
			for _, column := range index.Columns {
				if !valid(column.OperatorClass) || !valid(column.Collation) || (column.Key && column.OperatorClass == nil) {
					return false
				}
			}
		}
		for _, trigger := range table.ConstraintTriggers {
			if trigger.Constraint == "" || !valid(&trigger.Function) || !valid(trigger.Index) || trigger.Index == nil {
				return false
			}
		}
	}
	for _, typ := range snapshot.Types {
		if !valid(&typ.Identity) || !valid(typ.Relation) || !valid(typ.Element) || !valid(typ.Array) || !valid(typ.Base) {
			return false
		}
	}
	return true
}

// CompareSchemaCatalog is pure and order-stable for object collections. Ordered
// column/key members, expressions, literals and constraint definitions stay exact.
func CompareSchemaCatalog(actual, reference SchemaCatalog) []SchemaCatalogDifference {
	differences := make([]SchemaCatalogDifference, 0)
	compare := func(kind string, actualObjects, referenceObjects map[string]any) {
		names := make([]string, 0, len(actualObjects)+len(referenceObjects))
		for name := range actualObjects {
			names = append(names, name)
		}
		for name := range referenceObjects {
			if _, found := actualObjects[name]; !found {
				names = append(names, name)
			}
		}
		sort.Strings(names)
		for _, name := range names {
			left, leftExists := actualObjects[name]
			right, rightExists := referenceObjects[name]
			if !leftExists || !rightExists {
				differences = append(differences, SchemaCatalogDifference{ObjectKind: kind, ObjectName: name, Field: "Presence"})
				continue
			}
			compareSchemaCatalogValue(reflect.ValueOf(left), reflect.ValueOf(right), "", func(field string) {
				differences = append(differences, SchemaCatalogDifference{ObjectKind: kind, ObjectName: name, Field: field})
			})
		}
	}
	tables := func(snapshot SchemaCatalog) map[string]any {
		result := make(map[string]any, len(snapshot.Tables))
		for _, table := range snapshot.Tables {
			// Copy before sorting so the caller's snapshot is never changed.
			table.Columns = append([]SchemaCatalogColumn(nil), table.Columns...)
			table.Constraints = append([]SchemaCatalogConstraint(nil), table.Constraints...)
			table.Indexes = append([]SchemaCatalogIndex(nil), table.Indexes...)
			table.ConstraintTriggers = append([]SchemaCatalogConstraintTrigger(nil), table.ConstraintTriggers...)
			sort.Slice(table.Columns, func(i, j int) bool { return table.Columns[i].Position < table.Columns[j].Position })
			sort.Slice(table.Constraints, func(i, j int) bool { return table.Constraints[i].Name < table.Constraints[j].Name })
			sort.Slice(table.Indexes, func(i, j int) bool { return table.Indexes[i].Identity.Name < table.Indexes[j].Identity.Name })
			sort.Slice(table.ConstraintTriggers, func(i, j int) bool {
				return table.ConstraintTriggers[i].Constraint < table.ConstraintTriggers[j].Constraint
			})
			result[table.Identity.Namespace+"."+table.Identity.Name] = table
		}
		return result
	}
	types := func(snapshot SchemaCatalog) map[string]any {
		result := make(map[string]any, len(snapshot.Types))
		for _, typ := range snapshot.Types {
			result[typ.Identity.Namespace+"."+typ.Identity.Name] = typ
		}
		return result
	}
	compare("table", tables(actual), tables(reference))
	compare("type", types(actual), types(reference))
	return differences
}

func compareSchemaCatalogValue(actual, reference reflect.Value, field string, report func(string)) {
	if reflect.DeepEqual(actual.Interface(), reference.Interface()) {
		return
	}
	switch actual.Kind() {
	case reflect.Struct:
		for i := 0; i < actual.NumField(); i++ {
			child := actual.Type().Field(i).Name
			if field != "" {
				child = field + "." + child
			}
			compareSchemaCatalogValue(actual.Field(i), reference.Field(i), child, report)
		}
	case reflect.Pointer:
		if actual.IsNil() || reference.IsNil() {
			report(field)
			return
		}
		compareSchemaCatalogValue(actual.Elem(), reference.Elem(), field, report)
	case reflect.Slice:
		if actual.Len() != reference.Len() {
			report(field + ".Length")
		}
		for i := 0; i < actual.Len() && i < reference.Len(); i++ {
			compareSchemaCatalogValue(actual.Index(i), reference.Index(i), fmt.Sprintf("%s[%d]", field, i), report)
		}
	default:
		report(field)
	}
}

// Reject categories outside the current ordinary-table/composite-array profile,
// including namespace-bound objects not represented by pg_class/pg_type and
// table-bound objects whose namespace dependency is indirect.
const schemaCatalogUnsupportedSQL = `
WITH owner_namespace AS (SELECT oid FROM pg_catalog.pg_namespace WHERE nspname = $1)
SELECT
 EXISTS (SELECT 1 FROM pg_catalog.pg_depend d JOIN owner_namespace n ON n.oid = d.refobjid
   WHERE d.refclassid = 'pg_catalog.pg_namespace'::regclass
     AND (d.objsubid <> 0 OR d.classid NOT IN
       ('pg_catalog.pg_class'::regclass, 'pg_catalog.pg_type'::regclass, 'pg_catalog.pg_constraint'::regclass)))
 OR EXISTS (SELECT 1 FROM pg_catalog.pg_class c JOIN owner_namespace n ON n.oid = c.relnamespace
   WHERE c.relkind NOT IN ('r', 'i') OR c.relispartition)
 OR EXISTS (SELECT 1 FROM pg_catalog.pg_type t JOIN owner_namespace n ON n.oid = t.typnamespace
   LEFT JOIN pg_catalog.pg_class r ON r.oid = t.typrelid
   LEFT JOIN pg_catalog.pg_type element_type ON element_type.oid = t.typelem
   LEFT JOIN pg_catalog.pg_class element_relation ON element_relation.oid = element_type.typrelid
   WHERE ((t.typtype = 'c' AND r.relkind = 'r' AND r.relnamespace = n.oid)
      OR (t.typcategory = 'A' AND element_type.typtype = 'c'
        AND element_relation.relkind = 'r' AND element_relation.relnamespace = n.oid)) IS NOT TRUE)
 OR EXISTS (SELECT 1 FROM pg_catalog.pg_constraint k JOIN owner_namespace n ON n.oid = k.connamespace
   LEFT JOIN pg_catalog.pg_class r ON r.oid = k.conrelid
   WHERE k.contype NOT IN ('c', 'p', 'u', 'n') OR r.relnamespace IS DISTINCT FROM n.oid)
 OR EXISTS (SELECT 1 FROM pg_catalog.pg_trigger tr JOIN pg_catalog.pg_class c ON c.oid = tr.tgrelid
   JOIN owner_namespace n ON n.oid = c.relnamespace
   LEFT JOIN pg_catalog.pg_constraint k ON k.oid = tr.tgconstraint
   LEFT JOIN pg_catalog.pg_proc proc ON proc.oid = tr.tgfoid
   LEFT JOIN pg_catalog.pg_namespace proc_namespace ON proc_namespace.oid = proc.pronamespace
   WHERE (tr.tgisinternal AND k.conrelid = c.oid AND k.contype IN ('p', 'u')
     AND k.condeferrable AND tr.tgconstrrelid = 0 AND tr.tgconstrindid = k.conindid
     AND proc_namespace.nspname = 'pg_catalog' AND proc.proname = 'unique_key_recheck'
     AND proc.pronargs = 0) IS NOT TRUE)
 OR EXISTS (SELECT 1 FROM pg_catalog.pg_policy p JOIN pg_catalog.pg_class c ON c.oid = p.polrelid
   JOIN owner_namespace n ON n.oid = c.relnamespace)
 OR EXISTS (SELECT 1 FROM pg_catalog.pg_rewrite rw JOIN pg_catalog.pg_class c ON c.oid = rw.ev_class
   JOIN owner_namespace n ON n.oid = c.relnamespace)
 OR EXISTS (SELECT 1 FROM pg_catalog.pg_inherits inh
   JOIN pg_catalog.pg_class c ON c.oid = inh.inhrelid OR c.oid = inh.inhparent
   JOIN owner_namespace n ON n.oid = c.relnamespace)
`

const schemaCatalogTablesSQL = `
SELECT COALESCE(jsonb_agg(jsonb_build_object(
 'Identity', jsonb_build_object('Namespace', 'SELF', 'Name', c.relname),
 'Kind', c.relkind::text, 'Persistence', c.relpersistence::text,
 'AccessMethod', am.amname, 'Options', ARRAY(SELECT unnest(c.reloptions) ORDER BY 1),
 'RowSecurity', c.relrowsecurity, 'ForceRowSecurity', c.relforcerowsecurity,
 'ReplicaIdentity', c.relreplident::text, 'Comment', pg_catalog.obj_description(c.oid, 'pg_class'),
 'Columns', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
   'Position', a.attnum, 'Name', a.attname,
   'Type', CASE WHEN a.attisdropped THEN NULL ELSE jsonb_build_object(
     'Namespace', CASE WHEN type_namespace.nspname = $1 THEN 'SELF' ELSE type_namespace.nspname END,
     'Name', column_type.typname) END,
   'TypeModifier', a.atttypmod, 'Dimensions', a.attndims,
   'Collation', CASE WHEN a.attcollation = 0 THEN NULL ELSE jsonb_build_object(
     'Namespace', CASE WHEN collation_namespace.nspname = $1 THEN 'SELF' ELSE collation_namespace.nspname END,
     'Name', collation_row.collname) END,
   'NotNull', a.attnotnull, 'Identity', a.attidentity::text, 'Generated', a.attgenerated::text,
   'Dropped', a.attisdropped, 'Default', pg_catalog.pg_get_expr(ad.adbin, ad.adrelid, false),
   'Storage', a.attstorage::text, 'Compression', a.attcompression::text,
   'Comment', pg_catalog.col_description(a.attrelid, a.attnum)
  ) ORDER BY a.attnum), '[]'::jsonb)
  FROM pg_catalog.pg_attribute a
  LEFT JOIN pg_catalog.pg_type column_type ON column_type.oid = a.atttypid
  LEFT JOIN pg_catalog.pg_namespace type_namespace ON type_namespace.oid = column_type.typnamespace
  LEFT JOIN pg_catalog.pg_collation collation_row ON collation_row.oid = a.attcollation
  LEFT JOIN pg_catalog.pg_namespace collation_namespace ON collation_namespace.oid = collation_row.collnamespace
  LEFT JOIN pg_catalog.pg_attrdef ad ON ad.adrelid = a.attrelid AND ad.adnum = a.attnum
  WHERE a.attrelid = c.oid AND a.attnum > 0),
 'Constraints', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
   'Name', k.conname, 'Kind', k.contype::text,
   'Columns', ARRAY(SELECT a.attname FROM unnest(k.conkey) WITH ORDINALITY AS member(attnum, position)
     JOIN pg_catalog.pg_attribute a ON a.attrelid = k.conrelid AND a.attnum = member.attnum ORDER BY member.position),
   'Validated', k.convalidated, 'Deferrable', k.condeferrable, 'Deferred', k.condeferred,
   'NoInherit', k.connoinherit, 'IsLocal', k.conislocal, 'InheritedCount', k.coninhcount,
   'Enforced', to_jsonb(k)->'conenforced', 'Period', to_jsonb(k)->'conperiod',
   'Definition', pg_catalog.pg_get_constraintdef(k.oid, false),
   'Expression', pg_catalog.pg_get_expr(k.conbin, k.conrelid, false),
   'Comment', pg_catalog.obj_description(k.oid, 'pg_constraint'),
   'Index', CASE WHEN k.conindid = 0 THEN NULL ELSE jsonb_build_object(
     'Namespace', CASE WHEN index_namespace.nspname = $1 THEN 'SELF' ELSE index_namespace.nspname END,
     'Name', constraint_index.relname) END
  ) ORDER BY k.conname), '[]'::jsonb)
  FROM pg_catalog.pg_constraint k
  LEFT JOIN pg_catalog.pg_class constraint_index ON constraint_index.oid = k.conindid
  LEFT JOIN pg_catalog.pg_namespace index_namespace ON index_namespace.oid = constraint_index.relnamespace
  WHERE k.conrelid = c.oid),
 'Indexes', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
   'Identity', jsonb_build_object('Namespace', CASE WHEN index_namespace.nspname = $1 THEN 'SELF' ELSE index_namespace.nspname END, 'Name', index_relation.relname),
   'AccessMethod', index_method.amname, 'Options', ARRAY(SELECT unnest(index_relation.reloptions) ORDER BY 1),
   'Unique', i.indisunique, 'Primary', i.indisprimary, 'Exclusion', i.indisexclusion, 'Immediate', i.indimmediate,
   'NullsNotDistinct', i.indnullsnotdistinct, 'Valid', i.indisvalid, 'Ready', i.indisready, 'Live', i.indislive,
   'ReplicaIdentity', i.indisreplident, 'Clustered', i.indisclustered,
   'Comment', pg_catalog.obj_description(i.indexrelid, 'pg_class'),
   'KeyCount', i.indnkeyatts, 'AttributeCount', i.indnatts,
   'Expressions', pg_catalog.pg_get_expr(i.indexprs, i.indrelid, false),
   'Predicate', pg_catalog.pg_get_expr(i.indpred, i.indrelid, false),
   'Columns', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
     'Position', member.position, 'Name', a.attname,
     'Definition', pg_catalog.pg_get_indexdef(i.indexrelid, member.position::integer, false),
     'Key', member.position <= i.indnkeyatts,
     'Options', COALESCE(i.indoption[(member.position - 1)::integer], 0),
     'OperatorClass', CASE WHEN member.position > i.indnkeyatts THEN NULL ELSE jsonb_build_object(
       'Namespace', CASE WHEN operator_namespace.nspname = $1 THEN 'SELF' ELSE operator_namespace.nspname END, 'Name', operator_class.opcname) END,
     'Collation', CASE WHEN COALESCE(i.indcollation[(member.position - 1)::integer], 0) = 0 THEN NULL ELSE jsonb_build_object(
       'Namespace', CASE WHEN key_collation_namespace.nspname = $1 THEN 'SELF' ELSE key_collation_namespace.nspname END, 'Name', key_collation.collname) END
    ) ORDER BY member.position), '[]'::jsonb)
    FROM unnest(i.indkey) WITH ORDINALITY AS member(attnum, position)
    LEFT JOIN pg_catalog.pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = member.attnum
    LEFT JOIN pg_catalog.pg_opclass operator_class ON operator_class.oid = i.indclass[(member.position - 1)::integer]
    LEFT JOIN pg_catalog.pg_namespace operator_namespace ON operator_namespace.oid = operator_class.opcnamespace
    LEFT JOIN pg_catalog.pg_collation key_collation ON key_collation.oid = i.indcollation[(member.position - 1)::integer]
    LEFT JOIN pg_catalog.pg_namespace key_collation_namespace ON key_collation_namespace.oid = key_collation.collnamespace)
  ) ORDER BY index_relation.relname), '[]'::jsonb)
  FROM pg_catalog.pg_index i
  JOIN pg_catalog.pg_class index_relation ON index_relation.oid = i.indexrelid
  JOIN pg_catalog.pg_namespace index_namespace ON index_namespace.oid = index_relation.relnamespace
  LEFT JOIN pg_catalog.pg_am index_method ON index_method.oid = index_relation.relam
  WHERE i.indrelid = c.oid),
 'ConstraintTriggers', (SELECT COALESCE(jsonb_agg(jsonb_build_object(
   'Constraint', k.conname,
   'Function', jsonb_build_object('Namespace', proc_namespace.nspname, 'Name', proc.proname),
   'TimingFlags', tr.tgtype, 'Enabled', tr.tgenabled::text,
   'Deferrable', tr.tgdeferrable, 'Deferred', tr.tginitdeferred,
   'Columns', ARRAY(SELECT a.attname FROM unnest(tr.tgattr) WITH ORDINALITY AS member(attnum, position)
     JOIN pg_catalog.pg_attribute a ON a.attrelid = tr.tgrelid AND a.attnum = member.attnum ORDER BY member.position),
   'Arguments', encode(tr.tgargs, 'hex'),
   'Index', jsonb_build_object('Namespace', CASE WHEN trigger_index_namespace.nspname = $1 THEN 'SELF' ELSE trigger_index_namespace.nspname END, 'Name', trigger_index.relname)
  ) ORDER BY k.conname), '[]'::jsonb)
  FROM pg_catalog.pg_trigger tr
  JOIN pg_catalog.pg_constraint k ON k.oid = tr.tgconstraint
  JOIN pg_catalog.pg_proc proc ON proc.oid = tr.tgfoid
  JOIN pg_catalog.pg_namespace proc_namespace ON proc_namespace.oid = proc.pronamespace
  LEFT JOIN pg_catalog.pg_class trigger_index ON trigger_index.oid = tr.tgconstrindid
  LEFT JOIN pg_catalog.pg_namespace trigger_index_namespace ON trigger_index_namespace.oid = trigger_index.relnamespace
  WHERE tr.tgrelid = c.oid)
 ) ORDER BY c.relname), '[]'::jsonb)
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_catalog.pg_am am ON am.oid = c.relam
WHERE n.nspname = $1 AND c.relkind = 'r'
`

const schemaCatalogTypesSQL = `
SELECT COALESCE(jsonb_agg(jsonb_build_object(
 'Identity', jsonb_build_object('Namespace', 'SELF', 'Name', t.typname),
 'Kind', t.typtype::text, 'Category', t.typcategory::text, 'Defined', t.typisdefined,
 'Length', t.typlen, 'ByValue', t.typbyval, 'Alignment', t.typalign::text,
 'Storage', t.typstorage::text, 'Delimiter', t.typdelim::text, 'NotNull', t.typnotnull,
 'Comment', pg_catalog.obj_description(t.oid, 'pg_type'),
 'Relation', CASE WHEN t.typrelid = 0 THEN NULL ELSE jsonb_build_object(
   'Namespace', CASE WHEN relation_namespace.nspname = $1 THEN 'SELF' ELSE relation_namespace.nspname END, 'Name', r.relname) END,
 'Element', CASE WHEN t.typelem = 0 THEN NULL ELSE jsonb_build_object(
   'Namespace', CASE WHEN element_namespace.nspname = $1 THEN 'SELF' ELSE element_namespace.nspname END, 'Name', element_type.typname) END,
 'Array', CASE WHEN t.typarray = 0 THEN NULL ELSE jsonb_build_object(
   'Namespace', CASE WHEN array_namespace.nspname = $1 THEN 'SELF' ELSE array_namespace.nspname END, 'Name', array_type.typname) END,
 'Base', CASE WHEN t.typbasetype = 0 THEN NULL ELSE jsonb_build_object(
   'Namespace', CASE WHEN base_namespace.nspname = $1 THEN 'SELF' ELSE base_namespace.nspname END, 'Name', base_type.typname) END
 ) ORDER BY t.typname), '[]'::jsonb)
FROM pg_catalog.pg_type t
JOIN pg_catalog.pg_namespace n ON n.oid = t.typnamespace
LEFT JOIN pg_catalog.pg_class r ON r.oid = t.typrelid
LEFT JOIN pg_catalog.pg_namespace relation_namespace ON relation_namespace.oid = r.relnamespace
LEFT JOIN pg_catalog.pg_type element_type ON element_type.oid = t.typelem
LEFT JOIN pg_catalog.pg_namespace element_namespace ON element_namespace.oid = element_type.typnamespace
LEFT JOIN pg_catalog.pg_type array_type ON array_type.oid = t.typarray
LEFT JOIN pg_catalog.pg_namespace array_namespace ON array_namespace.oid = array_type.typnamespace
LEFT JOIN pg_catalog.pg_type base_type ON base_type.oid = t.typbasetype
LEFT JOIN pg_catalog.pg_namespace base_namespace ON base_namespace.oid = base_type.typnamespace
WHERE n.nspname = $1
`
