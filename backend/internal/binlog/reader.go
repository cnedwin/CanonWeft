package binlog

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-mysql-org/go-mysql/client"
	gomysql "github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"
	driver "github.com/go-sql-driver/mysql"
)

type Reader struct {
	serverUUID string
	address    string
	user       string
	password   string
	serverID   uint32
}

var serverIdentity = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// ReadTimeout bounds one observed source range. Callers that synchronously
// process transactions must give that work the same cancellation budget.
const ReadTimeout = 2 * time.Minute

func NewReader(dsn, serverUUID string) (*Reader, error) {
	if !serverIdentity.MatchString(serverUUID) {
		return nil, errors.New("verified source server identity required")
	}
	config, err := driver.ParseDSN(dsn)
	if err != nil || config.Net != "tcp" || config.User == "" {
		return nil, errors.New("invalid log reader connection")
	}
	host, portText, err := net.SplitHostPort(config.Addr)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		return nil, errors.New("log reader requires the local MySQL runtime")
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return nil, errors.New("invalid log reader endpoint")
	}
	var seed [4]byte
	if _, err = rand.Read(seed[:]); err != nil {
		return nil, errors.New("log reader identity unavailable")
	}
	serverID := binary.LittleEndian.Uint32(seed[:])
	if serverID < 2 {
		serverID += 2
	}
	return &Reader{serverUUID: serverUUID, address: config.Addr, user: config.User, password: config.Passwd, serverID: serverID}, nil
}

func (r *Reader) ReadRange(ctx context.Context, database string, tables []TableSchema, start, end Position, visit func(Transaction) error) (resultErr error) {
	code, stage, point, kind := "READ_CONTRACT_REJECTED", "validate", start, uint8(0)
	defer func() {
		if resultErr != nil {
			resultErr = readFailure(code, stage, point, kind, resultErr)
		}
	}()
	if r == nil || visit == nil {
		return errors.New("log reader and transaction consumer required")
	}
	decoder, err := NewDecoder(database, tables, start)
	if err != nil {
		return err
	}
	order, err := comparePositions(start, end)
	if err != nil || order > 0 {
		return errors.New("invalid source log range")
	}
	reading, cancel := context.WithTimeout(ctx, ReadTimeout)
	defer cancel()
	// One physical socket covers authentication, verification and streaming.
	// Cancellation interrupts handshake IO too; teardown never reconnects/KILLs.
	var socket net.Conn
	var stopCancellation func() bool
	defer func() {
		if stopCancellation != nil {
			stopCancellation()
		}
		if socket != nil {
			_ = socket.Close()
		}
	}()
	dialer := &net.Dialer{}
	code, stage = "SOURCE_CONNECTION_FAILED", "connect"
	connection, err := client.ConnectWithDialer(reading, "tcp", r.address, r.user, r.password, "", func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, dialErr := dialer.DialContext(ctx, network, address)
		if dialErr != nil {
			return nil, dialErr
		}
		socket = conn
		stopCancellation = context.AfterFunc(reading, func() { _ = conn.Close() })
		return conn, nil
	}, func(c *client.Conn) error {
		c.UnsetCapability(gomysql.CLIENT_LOCAL_FILES)
		return nil
	})
	if err != nil {
		return safeReadFailure(reading)
	}
	code, stage = "SOURCE_CONTRACT_REJECTED", "preflight"
	if err = r.preflight(reading, connection); err != nil {
		return err
	}
	if order == 0 {
		return observeEmpty(reading, connection, start)
	}
	code, stage = "STREAM_SETUP_FAILED", "stream_setup"
	result, err := connection.Execute("SET @master_binlog_checksum='NONE', @source_binlog_checksum='NONE'")
	if err != nil {
		return safeReadFailure(reading)
	}
	result.Close()
	// Payload: command, position, nonblocking flag, server ID, file.
	// EOF is a coverage failure unless the exact end was observed.
	packet := make([]byte, 15+len(start.File))
	packet[4] = gomysql.COM_BINLOG_DUMP
	binary.LittleEndian.PutUint32(packet[5:9], start.Offset)
	binary.LittleEndian.PutUint16(packet[9:11], replication.BINLOG_DUMP_NON_BLOCK)
	binary.LittleEndian.PutUint32(packet[11:15], r.serverID)
	copy(packet[15:], start.File)
	connection.ResetSequence()
	if err = connection.WritePacket(packet); err != nil {
		return safeReadFailure(reading)
	}
	parser := replication.NewBinlogParser()
	parser.SetFlavor("mysql")
	parser.SetVerifyChecksum(true)
	parser.SetTimestampStringLocation(time.UTC)
	parser.SetRenderJSONAsMySQLText(true)
	formatSeen := false
	for {
		code, stage, point, kind = "SOURCE_PACKET_UNAVAILABLE", "read_packet", decoder.position, 0
		if reading.Err() != nil {
			return reading.Err()
		}
		packet, readErr := connection.ReadPacket()
		if readErr != nil || len(packet) == 0 || packet[0] != gomysql.OK_HEADER {
			return safeReadFailure(reading)
		}
		code, stage = "PARSE_EVENT_REJECTED", "parse"
		if len(packet) > 5 {
			kind = packet[5]
		}
		event, parseErr := parseSourceEvent(parser, packet[1:], &formatSeen)
		if parseErr != nil {
			return parseErr
		}
		code, stage = "DECODE_EVENT_REJECTED", "decode"
		transaction, decodeErr := decoder.Consume(event)
		if decodeErr != nil {
			return decodeErr
		}
		code, stage = "RECOVERY_BOUNDARY_REJECTED", "boundary"
		order, err = comparePositions(decoder.position, end)
		if err != nil || order > 0 {
			return errors.New("requested recovery boundary is not a source event boundary")
		}
		if transaction != nil {
			code, stage = "TRANSACTION_REJECTED", "consume"
			if reading.Err() != nil {
				return reading.Err()
			}
			if err = visit(*transaction); err != nil {
				return err
			}
		}
		if order == 0 {
			if decoder.pending != nil {
				return errors.New("requested recovery boundary splits a transaction")
			}
			return reading.Err()
		}
	}
}

func safeReadFailure(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.New("source log unavailable; verify retained coordinates and reader permissions")
}

func (r *Reader) preflight(ctx context.Context, connection *client.Conn) error {
	result, err := connection.Execute("SELECT @@log_bin,@@binlog_format,@@binlog_row_image,@@binlog_row_metadata,@@binlog_checksum,@@binlog_row_value_options,@@binlog_transaction_compression,@@server_uuid")
	if err != nil {
		return safeReadFailure(ctx)
	}
	defer result.Close()
	for index, expected := range []string{"1", "ROW", "FULL", "FULL", "CRC32", "", "0", r.serverUUID} {
		actual, valueErr := result.GetString(0, index)
		if valueErr != nil || actual != expected {
			return errors.New("source identity or full ROW logging contract changed")
		}
	}
	return ctx.Err()
}

// Equal coordinates are evidence only when independently observed as the
// current completed source boundary. Historical empty claims are rejected.
func observeEmpty(ctx context.Context, connection *client.Conn, expected Position) error {
	result, err := connection.Execute("SHOW BINARY LOG STATUS")
	if err != nil {
		return safeReadFailure(ctx)
	}
	defer result.Close()
	file, fileErr := result.GetString(0, 0)
	offset, offsetErr := result.GetUint(0, 1)
	if fileErr != nil || offsetErr != nil || file != expected.File || offset != uint64(expected.Offset) {
		return errors.New("empty range is not the observed current source boundary")
	}
	return ctx.Err()
}

func parseSourceEvent(parser *replication.BinlogParser, raw []byte, formatSeen *bool) (event *replication.BinlogEvent, err error) {
	// A dependency error path prints partial JSON bytes directly. Reject
	// unsupported types before entering any upstream payload decoder.
	if len(raw) < replication.EventHeaderSize || len(raw) > 16<<20 || binary.LittleEndian.Uint32(raw[9:13]) != uint32(len(raw)) {
		return nil, errors.New("invalid source event envelope")
	}
	kind := replication.EventType(raw[4])
	switch kind {
	case replication.ROTATE_EVENT, replication.FORMAT_DESCRIPTION_EVENT:
	case replication.PREVIOUS_GTIDS_EVENT, replication.ANONYMOUS_GTID_EVENT, replication.GTID_EVENT,
		replication.QUERY_EVENT, replication.TABLE_MAP_EVENT, replication.WRITE_ROWS_EVENTv2,
		replication.UPDATE_ROWS_EVENTv2, replication.DELETE_ROWS_EVENTv2, replication.XID_EVENT:
		if !*formatSeen {
			return nil, errors.New("source event lacks validated format metadata")
		}
	default:
		return nil, errors.New("unsupported source event envelope")
	}
	defer func() {
		if recover() != nil {
			event = nil
			err = errors.New("invalid source event payload")
		}
	}()
	event, err = parser.Parse(raw)
	if err != nil {
		return nil, errors.New("source event decoding failed")
	}
	if kind == replication.FORMAT_DESCRIPTION_EVENT {
		format, ok := event.Event.(*replication.FormatDescriptionEvent)
		if !ok || format.Version != 4 || format.EventHeaderLength != replication.EventHeaderSize || format.ChecksumAlgorithm != replication.BINLOG_CHECKSUM_ALG_CRC32 {
			return nil, errors.New("source format requires version 4 with CRC32 integrity")
		}
		*formatSeen = true
	}
	return event, nil
}

func comparePositions(a, b Position) (int, error) {
	if !validPosition(a) || !validPosition(b) {
		return 0, errors.New("invalid source log position")
	}
	ai, bi := strings.LastIndexByte(a.File, '.'), strings.LastIndexByte(b.File, '.')
	if a.File[:ai] != b.File[:bi] {
		return 0, errors.New("source log file family changed")
	}
	an, ae := strconv.ParseUint(a.File[ai+1:], 10, 64)
	bn, be := strconv.ParseUint(b.File[bi+1:], 10, 64)
	if ae != nil || be != nil {
		return 0, errors.New("invalid source log sequence")
	}
	if an < bn {
		return -1, nil
	}
	if an > bn {
		return 1, nil
	}
	if a.File != b.File {
		return 0, errors.New("ambiguous source log file identity")
	}
	if a.Offset < b.Offset {
		return -1, nil
	}
	if a.Offset > b.Offset {
		return 1, nil
	}
	return 0, nil
}
