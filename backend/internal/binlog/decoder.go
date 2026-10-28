// Package binlog decodes complete MySQL transactions into bounded typed rows.
// Callers must sanitize application secrets before persisting these rows.
package binlog

import (
	"errors"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-mysql-org/go-mysql/replication"
)

type Position struct {
	File   string `json:"file"`
	Offset uint32 `json:"offset"`
}
type TableSchema struct {
	Name       string   `json:"name"`
	Columns    []string `json:"columns"`
	PrimaryKey []int    `json:"primary_key"`
}
type Row [][]byte
type Change struct {
	Table  string `json:"table"`
	Kind   string `json:"kind"`
	Before Row    `json:"before,omitempty"`
	After  Row    `json:"after,omitempty"`
}
type Transaction struct {
	Start       Position  `json:"start"`
	End         Position  `json:"end"`
	CommittedAt time.Time `json:"committed_at"`
	Changes     []Change  `json:"changes"`
}
type Decoder struct {
	database     string
	tables       map[string]TableSchema
	position     Position
	boundary     Position
	pending      *Transaction
	failed       bool
	begun        bool
	pendingBytes int64
}

func NewDecoder(database string, tables []TableSchema, start Position) (*Decoder, error) {
	if !identifier.MatchString(database) || len(tables) == 0 || len(tables) > 512 || !validPosition(start) {
		return nil, errors.New("invalid binary log baseline contract")
	}
	d := &Decoder{database: database, tables: make(map[string]TableSchema), position: start, boundary: start}
	for _, table := range tables {
		if _, exists := d.tables[table.Name]; exists || !identifier.MatchString(table.Name) || len(table.Columns) == 0 || len(table.Columns) > 1024 || len(table.PrimaryKey) == 0 {
			return nil, errors.New("invalid or duplicate source table contract")
		}
		columns := make(map[string]bool, len(table.Columns))
		for _, column := range table.Columns {
			if !identifier.MatchString(column) || columns[column] {
				return nil, errors.New("invalid or duplicate source column")
			}
			columns[column] = true
		}
		keys := make(map[int]bool, len(table.PrimaryKey))
		for _, index := range table.PrimaryKey {
			if index < 0 || index >= len(table.Columns) || keys[index] {
				return nil, errors.New("invalid source primary key")
			}
			keys[index] = true
		}
		table.Columns = slices.Clone(table.Columns)
		table.PrimaryKey = slices.Clone(table.PrimaryKey)
		d.tables[table.Name] = table
	}
	return d, nil
}

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
var binaryLogName = regexp.MustCompile(`^[A-Za-z0-9_-]+\.[0-9]{6,}$`)

func validPosition(position Position) bool {
	return position.Offset >= 4 && len(position.File) <= 255 && binaryLogName.MatchString(position.File)
}
func (d *Decoder) Consume(event *replication.BinlogEvent) (completed *Transaction, err error) {
	if d.failed {
		return nil, errors.New("binary log stream requires restart from a verified point")
	}
	defer func() {
		if err != nil {
			d.failed = true
			d.pending = nil
		}
	}()
	if event == nil || event.Header == nil {
		return nil, errors.New("invalid binary log event")
	}
	if len(event.RawData) > 16<<20 {
		return nil, errors.New("binary log event capacity exceeded")
	}
	if rotation, ok := event.Event.(*replication.RotateEvent); ok {
		if d.pending != nil || rotation.Position > math.MaxUint32 {
			return nil, errors.New("invalid binary log rotation")
		}
		next := Position{File: string(rotation.NextLogName), Offset: uint32(rotation.Position)}
		// MySQL repeats this synthetic notification after a real rotation.
		// It can only reaffirm the exact current position, never advance it.
		if event.Header.LogPos == 0 && next == d.position {
			return nil, nil
		}
		if !validPosition(next) || next.Offset != 4 || event.Header.LogPos <= d.position.Offset || !nextLog(d.position.File, next.File) {
			return nil, errors.New("binary log rotation gap")
		}
		d.position = next
		return nil, nil
	}
	if _, ok := event.Event.(*replication.FormatDescriptionEvent); ok {
		if d.pending != nil || (event.Header.LogPos != 0 && event.Header.LogPos < 4) {
			return nil, errors.New("invalid binary log format boundary")
		}
		// On reconnect the server sends the file's original format header,
		// whose position can be zero or precede the requested durable cursor.
		// Such metadata never advances the recovery position.
		if event.Header.LogPos > d.position.Offset {
			d.position.Offset = event.Header.LogPos
		}
		return nil, nil
	}
	if event.Header.LogPos <= d.position.Offset {
		return nil, errors.New("nonadvancing binary log cursor")
	}
	end := Position{File: d.position.File, Offset: event.Header.LogPos}
	switch value := event.Event.(type) {
	case *replication.TableMapEvent:
		if d.pending == nil || !d.begun {
			return nil, errors.New("table mapping outside a transaction")
		}
	case *replication.PreviousGTIDsEvent:
		if d.pending != nil {
			return nil, errors.New("unexpected prior transaction metadata")
		}
	case *replication.GTIDEvent:
		if d.pending != nil || value.ImmediateCommitTimestamp == 0 || value.ImmediateCommitTimestamp > 253402300799999999 {
			return nil, errors.New("transaction commit identity unavailable")
		}
		d.pending = &Transaction{Start: d.boundary, CommittedAt: time.UnixMicro(int64(value.ImmediateCommitTimestamp)).UTC()}
		d.pendingBytes = 0
	case *replication.QueryEvent:
		if d.pending == nil || d.begun || strings.TrimSpace(string(value.Query)) != "BEGIN" {
			return nil, errors.New("unsupported binary log statement")
		}
		d.begun = true
	case *replication.RowsEvent:
		if d.pending == nil || !d.begun || value.Table == nil {
			return nil, errors.New("row outside a complete transaction")
		}
		if string(value.Table.Schema) == d.database {
			table, exists := d.tables[string(value.Table.Table)]
			if !exists {
				return nil, errors.New("unsupported source row event")
			}
			if len(value.Rows) > 100000-len(d.pending.Changes) {
				return nil, errors.New("binary log transaction row capacity exceeded")
			}
			payloadBytes, sizeErr := rowPayloadBytes(value.Rows)
			if sizeErr != nil {
				return nil, sizeErr
			}
			if payloadBytes > 64<<20-d.pendingBytes {
				return nil, errors.New("binary log transaction byte capacity exceeded")
			}
			if err = validateRows(table, value); err != nil {
				return nil, err
			}
			changes, decodeErr := rowChanges(table.Name, event.Header.EventType, value)
			if decodeErr != nil {
				return nil, decodeErr
			}
			// ENUM/SET labels may be much larger than their wire ordinals.
			var decodedBytes int64
			for _, change := range changes {
				for _, row := range []Row{change.Before, change.After} {
					for _, cell := range row {
						decodedBytes += int64(len(cell)) + 32
					}
				}
			}
			if decodedBytes > 64<<20-d.pendingBytes {
				return nil, errors.New("binary log transaction byte capacity exceeded")
			}
			d.pending.Changes = append(d.pending.Changes, changes...)
			d.pendingBytes += decodedBytes
		}
	case *replication.XIDEvent:
		if d.pending == nil || !d.begun {
			return nil, errors.New("commit without transaction identity")
		}
		d.pending.End = end
		completed = d.pending
		d.pending = nil
		d.begun = false
		d.boundary = end
		d.pendingBytes = 0
	default:
		return nil, errors.New("unsupported binary log event")
	}
	d.position = end
	return completed, nil
}

// Count before copying payloads. Numeric allowance also bounds row overhead;
// the decoded raw event itself is limited separately at the reader boundary.
func rowPayloadBytes(rows [][]any) (int64, error) {
	var total int64
	for _, row := range rows {
		for _, cell := range row {
			total += 32
			switch value := cell.(type) {
			case string:
				total += int64(len(value))
			case []byte:
				total += int64(len(value))
			}
			if total > 16<<20 {
				return 0, errors.New("binary log row event capacity exceeded")
			}
		}
	}
	return total, nil
}

func nextLog(previous, next string) bool {
	oldDot, newDot := strings.LastIndexByte(previous, '.'), strings.LastIndexByte(next, '.')
	if oldDot < 0 || newDot < 0 || previous[:oldDot] != next[:newDot] {
		return false
	}
	oldNumber, oldErr := strconv.ParseUint(previous[oldDot+1:], 10, 64)
	newNumber, newErr := strconv.ParseUint(next[newDot+1:], 10, 64)
	return oldErr == nil && newErr == nil && oldNumber < math.MaxUint64 && newNumber == oldNumber+1
}

func validateRows(table TableSchema, event *replication.RowsEvent) error {
	metadata := event.Table
	if int(event.ColumnCount) != len(table.Columns) || int(metadata.ColumnCount) != len(table.Columns) || len(metadata.ColumnName) != len(table.Columns) || len(metadata.PrimaryKey) != len(table.PrimaryKey) || len(table.PrimaryKey) == 0 || len(event.Rows) == 0 {
		return errors.New("incomplete source row metadata")
	}
	// The pinned parser's label-map helpers index these arrays directly.
	// Validate their dimensions before calling them, including NULL-only rows.
	if len(metadata.ColumnType) != len(table.Columns) || len(metadata.ColumnMeta) != len(table.Columns) {
		return errors.New("incomplete source type metadata")
	}
	enums, sets := 0, 0
	for i := range table.Columns {
		if metadata.IsEnumColumn(i) {
			enums++
		}
		if metadata.IsSetColumn(i) {
			sets++
		}
	}
	if enums != len(metadata.EnumStrValue) || sets != len(metadata.SetStrValue) {
		return errors.New("incomplete enum or set metadata")
	}
	for _, labels := range metadata.EnumStrValue {
		if len(labels) == 0 || len(labels) > 65535 {
			return errors.New("invalid enum metadata")
		}
	}
	for _, labels := range metadata.SetStrValue {
		if len(labels) == 0 || len(labels) > 64 {
			return errors.New("invalid set metadata")
		}
	}
	for i, name := range table.Columns {
		if string(metadata.ColumnName[i]) != name {
			return errors.New("source column identity changed")
		}
	}
	for i, index := range table.PrimaryKey {
		if metadata.PrimaryKey[i] != uint64(index) {
			return errors.New("source primary key changed")
		}
	}
	if len(metadata.PrimaryKeyPrefix) != 0 && len(metadata.PrimaryKeyPrefix) != len(table.PrimaryKey) {
		return errors.New("incomplete source key metadata")
	}
	for _, prefix := range metadata.PrimaryKeyPrefix {
		if prefix != 0 {
			return errors.New("prefix primary keys are unsupported")
		}
	}
	for _, skipped := range event.SkippedColumns {
		if len(skipped) != 0 {
			return errors.New("partial row image is unsupported")
		}
	}
	for _, row := range event.Rows {
		if len(row) != len(table.Columns) {
			return errors.New("incomplete source row image")
		}
	}
	return nil
}

func rowChanges(table string, kind replication.EventType, event *replication.RowsEvent) ([]Change, error) {
	values := event.Rows
	enums, sets := event.Table.EnumStrValueMap(), event.Table.SetStrValueMap()
	var decodedBytes int64
	step := 1
	switch kind {
	case replication.WRITE_ROWS_EVENTv2, replication.DELETE_ROWS_EVENTv2:
	case replication.UPDATE_ROWS_EVENTv2:
		step = 2
		if len(values)%2 != 0 {
			return nil, errors.New("unpaired update row images")
		}
	default:
		return nil, errors.New("unsupported source row operation")
	}
	changes := make([]Change, 0, len(values)/step)
	for i := 0; i < len(values); i += step {
		row, err := decodeRow(values[i], enums, sets, &decodedBytes)
		if err != nil {
			return nil, err
		}
		change := Change{Table: table}
		switch kind {
		case replication.WRITE_ROWS_EVENTv2:
			change.Kind = "insert"
			change.After = row
		case replication.DELETE_ROWS_EVENTv2:
			change.Kind = "delete"
			change.Before = row
		case replication.UPDATE_ROWS_EVENTv2:
			change.Kind = "update"
			change.Before = row
			change.After, err = decodeRow(values[i+1], enums, sets, &decodedBytes)
			if err != nil {
				return nil, err
			}
		}
		changes = append(changes, change)
	}
	return changes, nil
}

func decodeRow(values []any, enums, sets map[int][]string, decodedBytes *int64) (Row, error) {
	row := make(Row, len(values))
	for i, value := range values {
		if value != nil {
			if labels, ok := enums[i]; ok {
				ordinal, valid := value.(int64)
				if !valid || ordinal < 0 || ordinal > int64(len(labels)) {
					return nil, errors.New("invalid enum ordinal")
				}
				value = "" // MySQL's zero error/sentinel value is non-NULL empty text.
				if ordinal > 0 {
					value = labels[ordinal-1]
				}
			} else if labels, ok := sets[i]; ok {
				packed, valid := value.(int64)
				mask := uint64(packed) // The pinned parser returns all 64 SET bits as int64.
				if !valid || (len(labels) < 64 && mask>>uint(len(labels)) != 0) {
					return nil, errors.New("invalid set bitmask")
				}
				var selected []string
				var size int64
				for bit, label := range labels {
					if mask&(uint64(1)<<uint(bit)) != 0 {
						if len(selected) != 0 {
							size++
						}
						size += int64(len(label))
						if size+*decodedBytes+32 > 16<<20 {
							return nil, errors.New("binary log row event capacity exceeded")
						}
						selected = append(selected, label)
					}
				}
				value = strings.Join(selected, ",")
			}
		}
		*decodedBytes += 32
		switch cell := value.(type) {
		case nil:
		case []byte:
			*decodedBytes += int64(len(cell))
			if *decodedBytes > 16<<20 {
				return nil, errors.New("binary log row event capacity exceeded")
			}
			row[i] = append([]byte{}, cell...)
		case string:
			*decodedBytes += int64(len(cell))
			if *decodedBytes > 16<<20 {
				return nil, errors.New("binary log row event capacity exceeded")
			}
			row[i] = []byte(cell)
		case int, int8, int16, int32, int64:
			row[i] = []byte(strconv.FormatInt(reflect.ValueOf(cell).Int(), 10))
			*decodedBytes += int64(len(row[i]))
		case uint, uint8, uint16, uint32, uint64:
			row[i] = []byte(strconv.FormatUint(reflect.ValueOf(cell).Uint(), 10))
			*decodedBytes += int64(len(row[i]))
		case float32, float64:
			value := reflect.ValueOf(cell)
			number := value.Float()
			if math.IsNaN(number) || math.IsInf(number, 0) {
				return nil, errors.New("nonfinite binary log value")
			}
			row[i] = []byte(strconv.FormatFloat(number, 'g', -1, value.Type().Bits()))
			*decodedBytes += int64(len(row[i]))
		default:
			return nil, errors.New("unsupported binary log cell type")
		}
		if *decodedBytes > 16<<20 {
			return nil, errors.New("binary log row event capacity exceeded")
		}
	}
	return row, nil
}
