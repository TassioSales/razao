package store

import "context"

// RawExec runs arbitrary SQL. Test-only.
func (s *Store) RawExec(ctx context.Context, sql string) error {
	_, err := s.pool.Exec(ctx, sql)
	return err
}
