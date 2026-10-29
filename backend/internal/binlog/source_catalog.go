package binlog

import (
	"errors"
	"math"
	"strings"

	"github.com/go-mysql-org/go-mysql/client"
)

type sourceLogFile struct {
	File      string
	Size      uint64
	Encrypted bool
}

type closedLogFile struct {
	Size uint32
	Next string
}

// A retained successor proves the previous file is closed. This is read on the
// same identity-checked physical connection before entering the dump protocol.
func readClosedLogEnds(connection *client.Conn, start, end Position) (map[string]closedLogFile, error) {
	result, err := connection.Execute("SHOW BINARY LOGS")
	if err != nil {
		return nil, errors.New("retained source log catalog unavailable")
	}
	defer result.Close()
	if result.RowNumber() < 1 || result.RowNumber() > 4096 {
		return nil, errors.New("retained source log catalog capacity exceeded")
	}
	files := make([]sourceLogFile, 0, result.RowNumber())
	for row := 0; row < result.RowNumber(); row++ {
		name, nameErr := result.GetString(row, 0)
		size, sizeErr := result.GetUint(row, 1)
		if nameErr != nil || sizeErr != nil {
			return nil, errors.New("invalid retained source log catalog")
		}
		encrypted := false
		if result.ColumnNumber() > 2 {
			value, valueErr := result.GetString(row, 2)
			if valueErr != nil || (!strings.EqualFold(value, "No") && !strings.EqualFold(value, "Yes")) {
				return nil, errors.New("unverified source log encryption metadata")
			}
			encrypted = strings.EqualFold(value, "Yes")
		}
		files = append(files, sourceLogFile{File: name, Size: size, Encrypted: encrypted})
	}
	return verifiedClosedLogEnds(files, start, end)
}

func verifiedClosedLogEnds(files []sourceLogFile, start, end Position) (map[string]closedLogFile, error) {
	if len(files) == 0 || len(files) > 4096 || !validPosition(start) || !validPosition(end) || start.File == end.File {
		return nil, errors.New("invalid source catalog range")
	}
	seen := make(map[string]bool, len(files))
	first, last := -1, -1
	for index, file := range files {
		if !validPosition(Position{File: file.File, Offset: 4}) || file.Size < 4 || file.Size > math.MaxUint32 || seen[file.File] {
			return nil, errors.New("invalid or ambiguous source catalog entry")
		}
		seen[file.File] = true
		if file.File == start.File {
			first = index
		}
		if file.File == end.File {
			last = index
		}
	}
	if first < 0 || last <= first || uint64(start.Offset) > files[first].Size || uint64(end.Offset) > files[last].Size {
		return nil, errors.New("requested source range is not retained")
	}
	closed := make(map[string]closedLogFile, last-first)
	for index := first; index < last; index++ {
		current, next := files[index], files[index+1]
		if current.Encrypted || next.Encrypted || !nextLog(current.File, next.File) {
			return nil, errors.New("unverified source log succession")
		}
		closed[current.File] = closedLogFile{Size: uint32(current.Size), Next: next.File}
	}
	return closed, nil
}
