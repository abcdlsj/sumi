package app

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/abcdlsj/sumi/internal/domain"
)

// agentNamePool seeds auto-assigned Agent display names. Every entry must pass
// domain.ValidateDisplayName so the generated member row is always valid.
var agentNamePool = []string{
	"Ada", "Alan", "Ava", "Bruno", "Clara", "Dante",
	"Elena", "Elias", "Fiona", "Gabriel", "Hazel", "Hugo",
	"Iris", "Jonas", "Julia", "Kai", "Lena", "Marco",
	"Mia", "Milo", "Nina", "Oscar", "Owen", "Paula",
	"Quinn", "Rosa", "Sam", "Tara", "Theo", "Vera",
}

// pickAgentDisplayNameTx returns the first pool name not yet used in the Space.
// Uniqueness is case-insensitive and covers retired members, matching
// idx_members_space_name. When the pool is exhausted it falls back to Agent<N>.
func pickAgentDisplayNameTx(ctx context.Context, tx *sql.Tx, spaceID string) (string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT lower(display_name) FROM members WHERE space_id = ?`, spaceID)
	if err != nil {
		return "", domain.WrapError(domain.CodeInternal, "list member names", err)
	}
	defer func() { _ = rows.Close() }()

	taken := make(map[string]struct{})
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return "", domain.WrapError(domain.CodeInternal, "scan member name", err)
		}
		taken[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return "", domain.WrapError(domain.CodeInternal, "list member names", err)
	}

	for _, name := range agentNamePool {
		if _, used := taken[strings.ToLower(name)]; !used {
			return name, nil
		}
	}
	for n := 1; ; n++ {
		name := fmt.Sprintf("Agent%d", n)
		if _, used := taken[strings.ToLower(name)]; !used {
			return name, nil
		}
	}
}
