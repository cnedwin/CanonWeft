package binlog_test

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"net"
	"novelstudio/internal/binlog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gomysql "github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"
	"github.com/go-mysql-org/go-mysql/server"
)

const fixtureUUID = "01000000-0000-0000-0000-000000000001"

// Deliberately slow or malformed local protocol peer. Never used by runtime.
type logPeer struct {
	listener    net.Listener
	stage       string
	raw         []byte
	reached     chan struct{}
	closed      chan struct{}
	connections atomic.Int32
	kills       atomic.Int32
	wait        sync.WaitGroup
	mu          sync.Mutex
	sockets     []net.Conn
	once        sync.Once
}

func newLogPeer(t *testing.T, stage string, raw []byte) *logPeer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &logPeer{listener: listener, stage: stage, raw: raw, reached: make(chan struct{}), closed: make(chan struct{})}
	p.wait.Add(1)
	go func() {
		defer p.wait.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			p.connections.Add(1)
			p.mu.Lock()
			p.sockets = append(p.sockets, conn)
			p.mu.Unlock()
			p.wait.Add(1)
			go func() { defer p.wait.Done(); p.serve(conn) }()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		p.mu.Lock()
		for _, conn := range p.sockets {
			_ = conn.Close()
		}
		p.mu.Unlock()
		p.wait.Wait()
	})
	return p
}

func (p *logPeer) stall(conn net.Conn) {
	p.once.Do(func() { close(p.reached) })
	_, _ = io.Copy(io.Discard, conn)
	select {
	case <-p.closed:
	default:
		close(p.closed)
	}
}

func (p *logPeer) serve(socket net.Conn) {
	defer socket.Close()
	if p.stage == "handshake" {
		p.stall(socket)
		return
	}
	conn, err := server.NewConn(socket, "reader", "fixture-password", server.EmptyHandler{})
	if err != nil {
		return
	}
	for {
		packet, err := conn.ReadPacket()
		if err != nil || len(packet) == 0 {
			return
		}
		if packet[0] == gomysql.COM_BINLOG_DUMP {
			if p.stage == "stream" {
				p.stall(socket)
				return
			}
			wire := append(make([]byte, 5), p.raw...)
			_ = conn.WritePacket(wire)
			return
		}
		if packet[0] != gomysql.COM_QUERY {
			_ = conn.WriteValue(nil)
			conn.ResetSequence()
			continue
		}
		query := string(packet[1:])
		if strings.HasPrefix(strings.ToUpper(query), "KILL ") {
			p.kills.Add(1)
		}
		var result *gomysql.Result
		if strings.HasPrefix(query, "SELECT @@log_bin") {
			if p.stage == "preflight" {
				p.stall(socket)
				return
			}
			rs, err := gomysql.BuildSimpleTextResultset([]string{"enabled", "format", "image", "metadata", "checksum", "options", "compressed", "uuid"}, [][]any{{1, "ROW", "FULL", "FULL", "CRC32", "", 0, fixtureUUID}})
			if err != nil {
				return
			}
			result = &gomysql.Result{Resultset: rs}
		} else if strings.HasPrefix(query, "SET @master_binlog_checksum") && p.stage == "setup" {
			p.stall(socket)
			return
		}
		if result == nil {
			err = conn.WriteValue(nil)
		} else {
			err = conn.WriteValue(result)
			result.Close()
		}
		if err != nil {
			return
		}
		conn.ResetSequence()
	}
}

func (p *logPeer) read(ctx context.Context) error {
	r, err := binlog.NewReader("reader:fixture-password@tcp("+p.listener.Addr().String()+")/", fixtureUUID)
	if err != nil {
		return err
	}
	return r.ReadRange(ctx, "target", []binlog.TableSchema{{Name: "documents", Columns: []string{"id", "body"}, PrimaryKey: []int{0}}}, binlog.Position{File: "binlog.000001", Offset: 4}, binlog.Position{File: "binlog.000001", Offset: 1000}, func(binlog.Transaction) error { return errors.New("unexpected transaction") })
}

func TestLogCancellationClosesOnlyOwnedConnection(t *testing.T) {
	for _, stage := range []string{"handshake", "preflight", "setup", "stream"} {
		t.Run(stage, func(t *testing.T) {
			peer := newLogPeer(t, stage, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			result := make(chan error, 1)
			go func() { result <- peer.read(ctx) }()
			select {
			case <-peer.reached:
			case <-time.After(3 * time.Second):
				t.Fatal("reader did not reach controlled IO stage")
			}
			cancel()
			select {
			case err := <-result:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation lost", err)
				}
			case <-time.After(time.Second):
				t.Fatal("canceled reader retained blocking IO")
			}
			select {
			case <-peer.closed:
			case <-time.After(time.Second):
				t.Fatal("owned socket remained open")
			}
			if peer.connections.Load() != 1 || peer.kills.Load() != 0 {
				t.Fatal("cleanup opened another connection or issued KILL")
			}
		})
	}
}

func TestPartialJSONEnvelopeRejectedBeforeDecoderDiagnostics(t *testing.T) {
	// Even bytes mentioning an unrelated database cannot reach the library's
	// partial JSON decoder. Capture output in this serial test process only.
	marker := "unrelated_database_sensitive_json_marker"
	raw := append(make([]byte, replication.EventHeaderSize), []byte(marker)...)
	raw[4] = byte(replication.PARTIAL_UPDATE_ROWS_EVENT)
	binary.LittleEndian.PutUint32(raw[9:13], uint32(len(raw)))
	peer := newLogPeer(t, "payload", raw)
	stdout, stderr := os.Stdout, os.Stderr
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = write, write
	defer func() { os.Stdout, os.Stderr = stdout, stderr; read.Close(); write.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err = peer.read(ctx)
	os.Stdout, os.Stderr = stdout, stderr
	write.Close()
	output, captureErr := io.ReadAll(read)
	if captureErr != nil || len(output) != 0 {
		t.Fatal("decoder emitted diagnostics containing source data")
	}
	info, diagnosed := binlog.ReadFailureInfo(err)
	if err == nil || !diagnosed || info.Code != "PARSE_EVENT_REJECTED" || info.Stage != "parse" || info.Position != (binlog.Position{File: "binlog.000001", Offset: 4}) || info.EventType != uint8(replication.PARTIAL_UPDATE_ROWS_EVENT) || strings.Contains(err.Error(), marker) {
		t.Fatal("unsupported raw payload entered the dependency decoder", err)
	}
	if peer.connections.Load() != 1 || peer.kills.Load() != 0 {
		t.Fatal("failed read reconnected for cleanup")
	}
}

func TestHistoricalFormatMustActuallyRequireAndPassCRC32(t *testing.T) {
	for _, test := range []struct {
		name      string
		algorithm byte
		version   uint16
		corrupt   bool
		valid     bool
	}{
		{"valid", 1, 4, false, true},
		{"checksum disabled", 0, 4, false, false},
		{"checksum undefined", 255, 4, false, false},
		{"unsupported format", 1, 3, false, false},
		{"corrupt checksum", 1, 4, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := make([]byte, replication.EventHeaderSize+57+41+5)
			raw[4] = byte(replication.FORMAT_DESCRIPTION_EVENT)
			binary.LittleEndian.PutUint32(raw[9:13], uint32(len(raw)))
			binary.LittleEndian.PutUint32(raw[13:17], 1000)
			payload := raw[replication.EventHeaderSize:]
			binary.LittleEndian.PutUint16(payload[:2], test.version)
			copy(payload[2:52], "8.4.13-test")
			payload[56] = replication.EventHeaderSize
			raw[len(raw)-5] = test.algorithm
			binary.LittleEndian.PutUint32(raw[len(raw)-4:], crc32.ChecksumIEEE(raw[:len(raw)-4]))
			if test.corrupt {
				raw[len(raw)-1] ^= 1
			}
			peer := newLogPeer(t, "format", raw)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err := peer.read(ctx)
			if (err == nil) != test.valid {
				t.Fatalf("format integrity acceptance=%t, wanted %t", err == nil, test.valid)
			}
		})
	}
}
