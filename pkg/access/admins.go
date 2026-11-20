package access

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

func parseAdmins(r io.Reader) (map[string]struct{}, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	admins := make(map[string]struct{})
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) != 1 {
			return nil, fmt.Errorf("admins line %d: want 1 field, got %d", lineNo, len(fields))
		}

		handle, err := ParseHandle(fields[0])
		if err != nil {
			return nil, fmt.Errorf("admins line %d: %w", lineNo, err)
		}
		if _, ok := admins[handle]; ok {
			return nil, fmt.Errorf("admins line %d: duplicate handle %q", lineNo, handle)
		}
		admins[handle] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return admins, nil
}
