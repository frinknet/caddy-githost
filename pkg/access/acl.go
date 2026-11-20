package access

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

func parseACL(org string, r io.Reader, knownHandle func(string) bool) (map[string]Role, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	acl := make(map[string]Role)
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
		if len(fields) != 2 {
			return nil, fmt.Errorf("%s.access line %d: want 2 fields, got %d", org, lineNo, len(fields))
		}

		handle, err := ParseHandle(fields[0])
		if err != nil {
			return nil, fmt.Errorf("%s.access line %d: %w", org, lineNo, err)
		}
		if !knownHandle(handle) {
			return nil, fmt.Errorf("%s.access line %d: unknown handle %q", org, lineNo, handle)
		}
		if _, ok := acl[handle]; ok {
			return nil, fmt.Errorf("%s.access line %d: duplicate handle %q", org, lineNo, handle)
		}

		role, err := ParseRole(fields[1])
		if err != nil {
			return nil, fmt.Errorf("%s.access line %d: %w", org, lineNo, err)
		}
		acl[handle] = role
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return acl, nil
}
