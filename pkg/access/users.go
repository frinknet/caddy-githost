package access

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	gossh "golang.org/x/crypto/ssh"
)

func parseUsers(r io.Reader) ([]*User, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var users []*User
	byHandle := make(map[string]struct{})
	byKey := make(map[string]struct{})
	lineNo := 0

	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) != 4 {
			return nil, fmt.Errorf("users line %d: want 4 fields, got %d", lineNo, len(fields))
		}

		handle, err := ParseHandle(fields[2])
		if err != nil {
			return nil, fmt.Errorf("users line %d: %w", lineNo, err)
		}
		if _, ok := byHandle[handle]; ok {
			return nil, fmt.Errorf("users line %d: duplicate handle %q", lineNo, handle)
		}

		key, _, _, _, err := gossh.ParseAuthorizedKey([]byte(fields[0] + " " + fields[1]))
		if err != nil {
			return nil, fmt.Errorf("users line %d: %w", lineNo, err)
		}
		if key.Type() != fields[0] {
			return nil, fmt.Errorf("users line %d: key type %q does not match %q", lineNo, key.Type(), fields[0])
		}
		fingerprint := gossh.FingerprintSHA256(key)
		if _, ok := byKey[fingerprint]; ok {
			return nil, fmt.Errorf("users line %d: duplicate key %s", lineNo, fingerprint)
		}

		byHandle[handle] = struct{}{}
		byKey[fingerprint] = struct{}{}
		users = append(users, &User{
			Handle: handle,
			Email:  fields[3],
			Key:    key,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return users, nil
}
